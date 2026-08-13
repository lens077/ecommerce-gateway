package client

import (
	"context"
	"encoding/json"
	"errors"
	"hash/crc32"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/go-kratos/gateway/constants"
	"github.com/go-kratos/gateway/proxy/debug"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/google/uuid"
)

var ErrCancelWatch = errors.New("cancel watch")
var globalServiceWatcher = newServiceWatcher()
var serviceWatchLog = log.NewHelper(log.With(log.GetLogger(), "source", "servicewatch"))

func init() {
	debug.Register("watcher", globalServiceWatcher)
}

func uuid4() string {
	return uuid.NewString()
}

func instancesSetHash(instances []*registry.ServiceInstance) string {
	sort.Slice(instances, func(i, j int) bool {
		return instances[i].ID < instances[j].ID
	})
	jsBytes, err := json.Marshal(instances)
	if err != nil {
		return ""
	}
	return strconv.FormatUint(uint64(crc32.ChecksumIEEE(jsBytes)), 10)
}

type watcherStatus struct {
	watcher           registry.Watcher
	ctx               context.Context
	cancel            context.CancelFunc
	selectedInstances []*registry.ServiceInstance
}

type serviceWatcher struct {
	lock          sync.RWMutex
	watcherStatus map[string]*watcherStatus
	appliers      map[string]map[string]Applier
}

func newServiceWatcher() *serviceWatcher {
	s := &serviceWatcher{
		watcherStatus: make(map[string]*watcherStatus),
		appliers:      make(map[string]map[string]Applier),
	}
	go s.proccleanup()
	return s
}

func (s *serviceWatcher) setSelectedCache(endpoint string, status *watcherStatus, instances []*registry.ServiceInstance) bool {
	s.lock.Lock()
	defer s.lock.Unlock()

	if s.watcherStatus[endpoint] != status {
		return false
	}
	status.selectedInstances = instances
	return true
}

func (s *serviceWatcher) getSelectedCache(endpoint string) ([]*registry.ServiceInstance, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	ws, ok := s.watcherStatus[endpoint]
	if ok {
		return ws.selectedInstances, true
	}
	return nil, false
}

func (s *serviceWatcher) getAppliers(endpoint string) (map[string]Applier, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	appliers, ok := s.appliers[endpoint]
	if ok {
		return appliers, true
	}
	return nil, false
}

type Applier interface {
	Callback([]*registry.ServiceInstance) error
	Canceled() bool
}

// Add listens for changes to the endpoint without blocking gateway startup.
func (s *serviceWatcher) Add(_ context.Context, discovery registry.Discovery, endpoint string, applier Applier) (watcherExisted bool) {
	s.lock.Lock()
	ws, watcherExisted := s.watcherStatus[endpoint]
	if !watcherExisted {
		watchCtx, cancel := context.WithCancel(context.Background())
		ws = &watcherStatus{ctx: watchCtx, cancel: cancel}
		s.watcherStatus[endpoint] = ws
	}

	if applier != nil {
		if _, ok := s.appliers[endpoint]; !ok {
			s.appliers[endpoint] = make(map[string]Applier)
		}
		s.appliers[endpoint][uuid4()] = applier
	}
	cachedInstances := ws.selectedInstances
	s.lock.Unlock()

	if watcherExisted {
		if len(cachedInstances) > 0 && applier != nil {
			serviceWatchLog.Infof("Using cached %d selected instances on endpoint: %s, hash: %s", len(cachedInstances), endpoint, instancesSetHash(cachedInstances))
			if err := applier.Callback(cachedInstances); err != nil {
				serviceWatchLog.Errorf("Failed to apply cached services on endpoint: %s, err: %+v", endpoint, err)
			}
		}
		return true
	}

	go s.watchLoop(ws.ctx, discovery, endpoint, ws)
	return false
}

// watchLoop recreates failed registry watchers and applies each successful update.
func (s *serviceWatcher) watchLoop(ctx context.Context, discovery registry.Discovery, endpoint string, status *watcherStatus) {
	failureCount := 0

	for {
		if err := ctx.Err(); err != nil {
			s.removeWatcher(endpoint, status)
			return
		}

		watcher, err := discovery.Watch(ctx, endpoint)
		if err == nil && !s.setWatcher(endpoint, status, watcher) {
			_ = watcher.Stop()
			return
		}

		if err == nil {
			serviceWatchLog.Infof("Succeeded to initialize watcher on endpoint: %s", endpoint)
			for {
				services, nextErr := watcher.Next()
				if nextErr != nil {
					err = nextErr
					break
				}
				failureCount = 0
				s.applyServices(endpoint, status, services)
			}
		}

		if watcher != nil {
			_ = watcher.Stop()
		}
		if errors.Is(err, context.Canceled) || ctx.Err() != nil {
			s.removeWatcher(endpoint, status)
			return
		}

		failureCount++
		retryDelay := calculateRetryDelay(failureCount, constants.DiscoveryMaxRetryDelay)
		if failureCount <= constants.DiscoveryLogThreshold || retryDelay != time.Second {
			serviceWatchLog.Errorf("Failed to watch on endpoint: %s, err: %+v, failure count: %d, will recreate after %v",
				endpoint, err, failureCount, retryDelay)
		}
		if !waitForRetry(ctx, retryDelay) {
			s.removeWatcher(endpoint, status)
			return
		}
	}
}

func (s *serviceWatcher) setWatcher(endpoint string, status *watcherStatus, watcher registry.Watcher) bool {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.watcherStatus[endpoint] != status {
		return false
	}
	status.watcher = watcher
	return true
}

func (s *serviceWatcher) removeWatcher(endpoint string, status *watcherStatus) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.watcherStatus[endpoint] == status {
		delete(s.watcherStatus, endpoint)
	}
}

func (s *serviceWatcher) applyServices(endpoint string, status *watcherStatus, services []*registry.ServiceInstance) {
	if len(services) == 0 {
		serviceWatchLog.Warnf("Empty services on endpoint: %s, this most likely no available instance in discovery", endpoint)
		return
	}
	serviceWatchLog.Infof("Received %d services on endpoint: %s, hash: %s", len(services), endpoint, instancesSetHash(services))
	if !s.setSelectedCache(endpoint, status, services) {
		return
	}
	s.doCallback(endpoint, services)
}

func waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func calculateRetryDelay(failureCount int, maxDelay time.Duration) time.Duration {
	steps := constants.DiscoveryRetrySteps

	index := failureCount - 1
	if index >= len(steps) {
		return maxDelay
	}

	delay := steps[index]
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

func (s *serviceWatcher) doCallback(endpoint string, services []*registry.ServiceInstance) {
	canceled := 0
	func() {
		s.lock.RLock()
		defer s.lock.RUnlock()
		for id, applier := range s.appliers[endpoint] {
			if err := applier.Callback(services); err != nil {
				if errors.Is(err, ErrCancelWatch) {
					canceled += 1
					serviceWatchLog.Warnf("appliers on endpoint: %s, id: %s is canceled, will delete later", endpoint, id)
					continue
				}
				serviceWatchLog.Errorf("Failed to call appliers on endpoint: %q: %+v", endpoint, err)
			}
		}
	}()
	if canceled <= 0 {
		return
	}
	serviceWatchLog.Warnf("There are %d canceled appliers on endpoint: %q, will be deleted later in cleanup proc", canceled, endpoint)
}

func (s *serviceWatcher) proccleanup() {
	doCleanup := func() {
		s.lock.RLock()
		endpoints := make([]string, 0, len(s.appliers))
		for endpoint := range s.appliers {
			endpoints = append(endpoints, endpoint)
		}
		s.lock.RUnlock()

		for _, endpoint := range endpoints {
			var cleanup []string
			func() {
				s.lock.RLock()
				defer s.lock.RUnlock()
				for id, applier := range s.appliers[endpoint] {
					if applier.Canceled() {
						cleanup = append(cleanup, id)
						serviceWatchLog.Warnf("applier on endpoint: %s, id: %s is canceled, will be deleted later", endpoint, id)
						continue
					}
				}
			}()
			if len(cleanup) <= 0 {
				continue
			}
			serviceWatchLog.Infof("Cleanup appliers on endpoint: %q with keys: %+v", endpoint, cleanup)
			func() {
				s.lock.Lock()
				defer s.lock.Unlock()
				appliers := s.appliers[endpoint]
				for _, id := range cleanup {
					delete(appliers, id)
				}
				serviceWatchLog.Infof("Succeeded to clean %d appliers on endpoint: %q, now %d appliers are available", len(cleanup), endpoint, len(appliers))
			}()
			s.stopWatcherIfUnused(endpoint)
		}
	}

	interval := constants.DiscoveryCleanupInterval
	for {
		// serviceWatchLog.Infof("Start to cleanup appliers on all endpoints for every %s", interval.String())
		time.Sleep(interval)
		doCleanup()
	}
}

func (s *serviceWatcher) stopWatcherIfUnused(endpoint string) {
	s.lock.Lock()
	if len(s.appliers[endpoint]) != 0 {
		s.lock.Unlock()
		return
	}
	delete(s.appliers, endpoint)
	status := s.watcherStatus[endpoint]
	delete(s.watcherStatus, endpoint)
	var watcher registry.Watcher
	if status != nil {
		watcher = status.watcher
	}
	s.lock.Unlock()

	if status == nil {
		return
	}
	status.cancel()
	if watcher != nil {
		_ = watcher.Stop()
	}
}

func (s *serviceWatcher) DebugHandler() http.Handler {
	debugMux := http.NewServeMux()
	debugMux.HandleFunc("/debug/watcher/nodes", func(w http.ResponseWriter, r *http.Request) {
		service := r.URL.Query().Get("service")
		nodes, _ := s.getSelectedCache(service)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(nodes)
	})
	debugMux.HandleFunc("/debug/watcher/appliers", func(w http.ResponseWriter, r *http.Request) {
		service := r.URL.Query().Get("service")
		appliers, _ := s.getAppliers(service)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(appliers)
	})
	return debugMux
}

func AddWatch(ctx context.Context, registry registry.Discovery, endpoint string, applier Applier) bool {
	// 为全局的服务发现添加一个监听任务 参数：registry 注册中心  endpoint 节点信息  applier 节点应用
	return globalServiceWatcher.Add(ctx, registry, endpoint, applier)
}
