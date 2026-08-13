package client

import (
	"testing"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/kratos/v2/selector"
)

func testNode(addr string) selector.Node {
	w := int64(10)
	return newNode(addr, config.Protocol_GRPC, &w, map[string]string{}, "v1", "test-service")
}

func newTestHealthChecker(addrs ...string) *healthChecker {
	nodes := make([]selector.Node, 0, len(addrs))
	for _, a := range addrs {
		nodes = append(nodes, testNode(a))
	}
	return NewHealthChecker(nodes).(*healthChecker)
}

func (hc *healthChecker) stateOf(addr string) (healthy bool, failures int) {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.healthyNodes[addr], hc.failureCount[addr]
}

// updateNodes 曾经是「全删再全加」,于是服务发现每推送一次就把所有节点的失败计数
// 清零。配上 15s 的兜底轮询和 10s 的检查间隔,计数永远攒不到 maxFailures,
// 健康检查器一个不健康节点都标不出来。这个用例守住增量语义。
func TestUpdateNodes_KeepsExistingNodeState(t *testing.T) {
	hc := newTestHealthChecker("10.0.0.1:9000", "10.0.0.2:9000")
	hc.maxFailures = 3

	// 让 10.0.0.1 攒满失败次数并被标记为不健康
	for range 3 {
		hc.markNodeFailure("10.0.0.1:9000")
	}
	if healthy, _ := hc.stateOf("10.0.0.1:9000"); healthy {
		t.Fatal("前置条件不成立:节点没有被标记为不健康")
	}

	// 服务发现推来同一份列表(实例没变)
	hc.updateNodes([]selector.Node{testNode("10.0.0.1:9000"), testNode("10.0.0.2:9000")})

	healthy, failures := hc.stateOf("10.0.0.1:9000")
	if healthy {
		t.Error("一次服务发现推送就把不健康节点赦免回健康了")
	}
	if failures != 3 {
		t.Errorf("失败计数被重置成 %d,期望保留 3", failures)
	}
}

func TestUpdateNodes_AddsAndRemoves(t *testing.T) {
	hc := newTestHealthChecker("10.0.0.1:9000")
	hc.markNodeFailure("10.0.0.1:9000")

	// 1 下线, 2 上线
	hc.updateNodes([]selector.Node{testNode("10.0.0.2:9000")})

	hc.mu.RLock()
	_, stillTracked := hc.nodes["10.0.0.1:9000"]
	_, staleHealth := hc.healthyNodes["10.0.0.1:9000"]
	_, staleFailure := hc.failureCount["10.0.0.1:9000"]
	nodeCount := len(hc.nodes)
	hc.mu.RUnlock()

	if stillTracked || staleHealth || staleFailure {
		t.Error("下线实例的状态没有被清干净,地址复用时会继承旧计数")
	}
	if nodeCount != 1 {
		t.Errorf("节点数为 %d,期望 1", nodeCount)
	}
	if healthy, failures := hc.stateOf("10.0.0.2:9000"); !healthy || failures != 0 {
		t.Errorf("新上线实例应初始化为健康/0 次失败,实得 healthy=%v failures=%d", healthy, failures)
	}
}

// 节点实例本身(权重、元数据)要跟着刷新,只是健康状态不动。
func TestUpdateNodes_RefreshesNodeInstance(t *testing.T) {
	hc := newTestHealthChecker("10.0.0.1:9000")
	hc.markNodeFailure("10.0.0.1:9000")

	replacement := testNode("10.0.0.1:9000")
	hc.updateNodes([]selector.Node{replacement})

	hc.mu.RLock()
	got := hc.nodes["10.0.0.1:9000"]
	hc.mu.RUnlock()

	if got != replacement {
		t.Error("同地址的节点实例没有被最新的替换掉")
	}
	if _, failures := hc.stateOf("10.0.0.1:9000"); failures != 1 {
		t.Errorf("失败计数被重置成 %d,期望保留 1", failures)
	}
}
