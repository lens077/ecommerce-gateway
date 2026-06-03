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
	initializedChan   chan struct{}
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

func (s *serviceWatcher) setSelectedCache(endpoint string, instances []*registry.ServiceInstance) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.watcherStatus[endpoint].selectedInstances = instances
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

// Add 监听任务 监听该端点在注册中心中的服务的变化
func (s *serviceWatcher) Add(ctx context.Context, discovery registry.Discovery, endpoint string, applier Applier) (watcherExisted bool) {
	s.lock.Lock()
	defer s.lock.Unlock()

	existed := func() bool {
		// 检查该端点是否已经被监听
		ws, ok := s.watcherStatus[endpoint]
		if ok {
			// 如果已经监听，则获取管道信号，检查是否初始化完成，如果没有，则阻塞，直到其他协程完成该监听的初始化
			// this channel is used to notify the caller that the service watcher is initialized and ready to use
			<-ws.initializedChan

			// 如果端点的实例列表不为空，则调用回调将实例信息写入到节点应用器中
			if len(ws.selectedInstances) > 0 {
				serviceWatchLog.Infof("Using cached %d selected instances on endpoint: %s, hash: %s", len(ws.selectedInstances), endpoint, instancesSetHash(ws.selectedInstances))
				applier.Callback(ws.selectedInstances)
				return true
			}

			return true
		}

		// 如果没有监听，新建一个监听
		ws = &watcherStatus{
			initializedChan: make(chan struct{}),
		}

		// 创建一个带超时的 context，用于初始化 watcher
		// 如果服务在 Consul 中不存在或不可达，watcher 初始化会在超时后失败
		// 这样可以避免网关启动时被某个不可用的后端服务阻塞
		initCtx, initCancel := context.WithTimeout(ctx, constants.DiscoveryInitTimeout)
		defer initCancel()

		// 根据对应的注册中心以及端点，初始化一个监听器
		watcher, err := discovery.Watch(initCtx, endpoint)
		if err != nil {
			serviceWatchLog.Errorf("Failed to initialize watcher on endpoint: %s, err: %+v (will retry in background)", endpoint, err)
			// 即使 watcher 初始化失败，也关闭 channel 以避免阻塞
			// 注意：这里不手动解锁，由外层 defer 统一处理
			close(ws.initializedChan)
			// 在后台继续尝试建立 watcher，这样服务恢复后可以自动重新发现
			// 由于 Add 函数即将返回，这里的 goroutine 会在 Add 返回后执行
			go func() {
				failureCount := 0
				maxRetryDelay := constants.DiscoveryMaxRetryDelay

				for {
					failureCount++
					retryDelay := calculateRetryDelay(failureCount, maxRetryDelay)

					// 使用新的 context 尝试重新建立 watcher
					retryCtx, retryCancel := context.WithTimeout(context.Background(), retryDelay)
					watcher, err := discovery.Watch(retryCtx, endpoint)
					retryCancel()
					if err != nil {
						// 控制日志输出频率：只在延迟变化时或失败次数较少时输出日志
						if failureCount <= constants.DiscoveryLogThreshold || retryDelay != time.Second {
							serviceWatchLog.Warnf("Retry failed to initialize watcher on endpoint: %s, err: %+v, failure count: %d, will retry after %v",
								endpoint, err, failureCount, retryDelay)
						}
						time.Sleep(retryDelay)
						continue
					}
					serviceWatchLog.Infof("Succeeded to initialize watcher on endpoint: %s after retry", endpoint)
					s.lock.Lock()
					ws.watcher = watcher
					s.watcherStatus[endpoint] = ws
					s.lock.Unlock()

					// 通知等待的 applier
					s.lock.Lock()
					appliers := s.appliers[endpoint]
					s.lock.Unlock()
					if len(appliers) > 0 {
						for id, applier := range appliers {
							serviceWatchLog.Infof("Notifying applier %s for endpoint: %s after watcher retry", id, endpoint)
							if services, ok := s.getSelectedCache(endpoint); ok && len(services) > 0 {
								applier.Callback(services)
							}
						}
					}

					// 启动后台监控
					go s.watchLoop(endpoint, watcher)
					return
				}
			}()
			return false
		}
		serviceWatchLog.Infof("Succeeded to initialize watcher on endpoint: %s", endpoint)
		ws.watcher = watcher
		s.watcherStatus[endpoint] = ws

		// 尝试从监听器中获取服务实例列表 如果失败直接退出匿名函数调用
		func() {
			defer close(ws.initializedChan)
			serviceWatchLog.Infof("Starting to do initialize services discovery on endpoint: %s", endpoint)
			services, err := watcher.Next()
			if err != nil {
				serviceWatchLog.Errorf("Failed to do initialize services discovery on endpoint: %s, err: %+v, the watch process will attempt asynchronously", endpoint, err)
				return
			}
			serviceWatchLog.Infof("Succeeded to do initialize services discovery on endpoint: %s, %d services, hash: %s", endpoint, len(services), instancesSetHash(ws.selectedInstances))
			ws.selectedInstances = services
			// 如果成功将服务实例列表写入到节点应用器中
			applier.Callback(services)
		}()

		// 开启协程，轮询监听器中的服务列表
		go s.watchLoop(endpoint, watcher)

		return false
	}()

	serviceWatchLog.Infof("Add appliers on endpoint: %s", endpoint)
	if applier != nil {
		if _, ok := s.appliers[endpoint]; !ok {
			s.appliers[endpoint] = make(map[string]Applier)
		}
		s.appliers[endpoint][uuid4()] = applier
	}

	return existed
}

// watchLoop 持续监听服务实例变化，当服务恢复时会自动通过 doCallback 通知所有 applier
func (s *serviceWatcher) watchLoop(endpoint string, watcher registry.Watcher) {
	failureCount := 0
	maxRetryDelay := constants.DiscoveryMaxRetryDelay

	for {
		services, err := watcher.Next()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				serviceWatchLog.Warnf("The watch process on: %s has been canceled", endpoint)
				s.lock.Lock()
				delete(s.watcherStatus, endpoint)
				s.lock.Unlock()
				return
			}

			failureCount++
			retryDelay := calculateRetryDelay(failureCount, maxRetryDelay)

			if failureCount <= constants.DiscoveryLogThreshold || retryDelay != time.Second {
				serviceWatchLog.Errorf("Failed to watch on endpoint: %s, err: %+v, failure count: %d, will retry after %v",
					endpoint, err, failureCount, retryDelay)
			}

			time.Sleep(retryDelay)
			continue
		}

		failureCount = 0

		if len(services) == 0 {
			serviceWatchLog.Warnf("Empty services on endpoint: %s, this most likely no available instance in discovery", endpoint)
			continue
		}
		serviceWatchLog.Infof("Received %d services on endpoint: %s, hash: %s", len(services), endpoint, instancesSetHash(services))
		s.setSelectedCache(endpoint, services)
		s.doCallback(endpoint, services)
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
		for endpoint, appliers := range s.appliers {
			var cleanup []string
			func() {
				s.lock.RLock()
				defer s.lock.RUnlock()
				for id, applier := range appliers {
					if applier.Canceled() {
						cleanup = append(cleanup, id)
						serviceWatchLog.Warnf("applier on endpoint: %s, id: %s is canceled, will be deleted later", endpoint, id)
						continue
					}
				}
			}()
			if len(cleanup) <= 0 {
				return
			}
			serviceWatchLog.Infof("Cleanup appliers on endpoint: %q with keys: %+v", endpoint, cleanup)
			func() {
				s.lock.Lock()
				defer s.lock.Unlock()
				for _, id := range cleanup {
					delete(appliers, id)
				}
				serviceWatchLog.Infof("Succeeded to clean %d appliers on endpoint: %q, now %d appliers are available", len(cleanup), endpoint, len(appliers))
			}()
		}
	}

	interval := constants.DiscoveryCleanupInterval
	for {
		serviceWatchLog.Infof("Start to cleanup appliers on all endpoints for every %s", interval.String())
		time.Sleep(interval)
		doCleanup()
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
