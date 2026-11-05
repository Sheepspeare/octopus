package relay

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/httpclient"
)

// testGroup 构造一个成员齐全的分组; 成员可用性在 GroupGetByName 阶段已过滤, 这里一律置为可用。
func testGroup(mode model.GroupMode, ids ...int) model.Group {
	group := model.Group{ID: 1, Name: "test", Mode: mode, RelayConfig: model.DefaultGroupRelayConfig()}
	for index, id := range ids {
		group.Items = append(group.Items, model.GroupItem{ID: id, GroupID: 1, Priority: index + 1, Available: true})
	}
	return group
}

// newTestGroup 建好路由状态并返回分组, 避免每个用例重复首轮选路。
func newTestGroup(t *testing.T, mode model.GroupMode, ids ...int) model.Group {
	t.Helper()
	ResetRouteState(1)
	t.Cleanup(func() { ResetRouteState(1) })
	group := testGroup(mode, ids...)
	pickGroupItem(group, nil)
	return group
}

// TestPickRoundRobinSkipsCoolingMember 验证轮转跳过冷却中的成员。
func TestPickRoundRobinSkipsCoolingMember(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2, 3)
	if !recordRouteFailure(group, 2, group.RelayConfig.MemberMaxAttempts, 0) {
		t.Fatalf("成员 2 达到尝试上限后应进入冷却")
	}

	for range 6 {
		if item := pickGroupItem(group, nil); item.ID == 2 {
			t.Fatalf("冷却中的成员不应被选中")
		}
	}
}

// TestPickRoundRobinAllCoolingReturnsZero 验证全部成员冷却时返回零值, 交给调用方等待而不是继续空转。
func TestPickRoundRobinAllCoolingReturnsZero(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2)
	for _, id := range []int{1, 2} {
		recordRouteFailure(group, id, group.RelayConfig.MemberMaxAttempts, 0)
	}

	if item := pickGroupItem(group, nil); item.ID != 0 {
		t.Fatalf("全部成员冷却时应返回零值, 实际返回 %d", item.ID)
	}
}

// TestPickRoundRobinSkipsExcludedMember 验证本请求内被排除的成员不再参与轮转。
func TestPickRoundRobinSkipsExcludedMember(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2, 3)
	exclude := map[int]bool{2: true}

	for range 6 {
		if item := pickGroupItem(group, exclude); item.ID == 2 {
			t.Fatalf("被排除的成员不应被选中")
		}
	}
}

// TestPickGroupItemManualWaitsForSelection 验证手动模式在未指定成员时返回零值, 由界面选定后继续。
func TestPickGroupItemManualWaitsForSelection(t *testing.T) {
	ResetRouteState(1)
	t.Cleanup(func() { ResetRouteState(1) })
	group := testGroup(model.GroupModeManual, 1, 2)

	if item := pickGroupItem(group, nil); item.ID != 0 {
		t.Fatalf("未指定成员时应返回零值, 实际返回 %d", item.ID)
	}
	group.ActiveItemID = 2
	if item := pickGroupItem(group, nil); item.ID != 2 {
		t.Fatalf("应选中人工指定的成员 2, 实际 %d", item.ID)
	}
}

// TestPickFailoverSkipsCoolingMember 验证故障转移按优先级跳过冷却中的成员。
func TestPickFailoverSkipsCoolingMember(t *testing.T) {
	group := newTestGroup(t, model.GroupModeFailover, 1, 2, 3)
	recordRouteFailure(group, 1, group.RelayConfig.MemberMaxAttempts, 0)

	if item := pickGroupItem(group, nil); item.ID != 2 {
		t.Fatalf("成员 1 冷却后应切到成员 2, 实际 %d", item.ID)
	}
}

// TestPickFailoverLeavesExcludedCurrentItem 验证当前成员被排除后不会继续被交回, 而是切到下一个成员。
func TestPickFailoverLeavesExcludedCurrentItem(t *testing.T) {
	group := newTestGroup(t, model.GroupModeFailover, 1, 2)
	if item := pickGroupItem(group, nil); item.ID != 1 {
		t.Fatalf("首轮应选中优先级最高的成员 1, 实际 %d", item.ID)
	}

	if item := pickGroupItem(group, map[int]bool{1: true}); item.ID != 2 {
		t.Fatalf("当前成员被排除后应切到成员 2, 实际 %d", item.ID)
	}
}

// TestRecordRouteFailureCountsPerMember 验证失败次数按成员累计: 未达上限不冷却, 达到上限才冷却。
func TestRecordRouteFailureCountsPerMember(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2)

	if recordRouteFailure(group, 1, 1, 0) {
		t.Fatalf("首次失败不应触发冷却")
	}
	if !recordRouteFailure(group, 1, 2, 0) {
		t.Fatalf("达到尝试上限应触发冷却")
	}
	if state := RouteStateOf(group); state.Cooldowns[1] == 0 {
		t.Fatalf("冷却表中的成员 1 应存在截止时间")
	}
}

// TestRecordRouteFailureUsesGivenCooldown 验证上游给出的重试间隔会覆盖分组配置的冷却时长。
func TestRecordRouteFailureUsesGivenCooldown(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1)
	before := time.Now()
	recordRouteFailure(group, 1, group.RelayConfig.MemberMaxAttempts, 5*time.Second)

	deadline := time.UnixMilli(RouteStateOf(group).Cooldowns[1])
	if deadline.Before(before.Add(4*time.Second)) || deadline.After(before.Add(6*time.Second)) {
		t.Fatalf("冷却截止时间应约为 5 秒后, 实际 %s", deadline.Sub(before))
	}
}

// TestMarkRouteSuspectBlocksMember 验证确定性 4xx 的临时冷却立刻让同批并发请求绕开该成员。
func TestMarkRouteSuspectBlocksMember(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2)
	markRouteSuspect(group, 1)

	for range 4 {
		if item := pickGroupItem(group, nil); item.ID == 1 {
			t.Fatalf("临时冷却中的成员不应被选中")
		}
	}
}

// TestClearRouteSuspectsReleasesMember 验证"问题在请求本身"时临时冷却被整条撤销。
func TestClearRouteSuspectsReleasesMember(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2)
	markRouteSuspect(group, 1)
	clearRouteSuspects(group, map[int]bool{1: true})

	if state := RouteStateOf(group); state.Cooldowns[1] != 0 {
		t.Fatalf("撤销后不应留下冷却")
	}
}

// TestRecordRouteFailureUpgradesProvisional 验证临时冷却会被按配置时长的正式冷却接管。
func TestRecordRouteFailureUpgradesProvisional(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1)
	markRouteSuspect(group, 1)
	recordRouteFailure(group, 1, group.RelayConfig.MemberMaxAttempts, 0)

	deadline := time.UnixMilli(RouteStateOf(group).Cooldowns[1])
	if deadline.Before(time.Now().Add(30 * time.Second)) {
		t.Fatalf("应升级为 60 秒的正式冷却, 实际截止 %s", deadline.Format(time.RFC3339))
	}
}

// TestAllItemsExcluded 验证"全部成员都被排除"的判定, 用于识别问题在请求本身。
func TestAllItemsExcluded(t *testing.T) {
	group := testGroup(model.GroupModeRoundRobin, 1, 2)
	if allItemsExcluded(group, map[int]bool{1: true}) {
		t.Fatalf("仍有成员未排除时不应判定为全部排除")
	}
	if !allItemsExcluded(group, map[int]bool{1: true, 2: true}) {
		t.Fatalf("全部成员排除后应判定为全部排除")
	}
	if allItemsExcluded(model.Group{}, map[int]bool{1: true}) {
		t.Fatalf("空分组不应判定为全部排除")
	}
}

// TestDeterministicClientStatus 验证 4xx 分类: 确定性拒绝换成员, 临时状态仍重试。
func TestDeterministicClientStatus(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		want   bool
	}{
		{name: "400 订阅失效", err: upstreamError(http.StatusBadRequest), status: http.StatusBadRequest, want: true},
		{name: "401 凭据无效", err: upstreamError(http.StatusUnauthorized), status: http.StatusUnauthorized, want: true},
		{name: "404 模型缺失", err: upstreamError(http.StatusNotFound), status: http.StatusNotFound, want: true},
		{name: "408 请求超时", err: upstreamError(http.StatusRequestTimeout), want: false},
		{name: "425 过早", err: upstreamError(http.StatusTooEarly), want: false},
		{name: "429 限流", err: upstreamError(http.StatusTooManyRequests), want: false},
		{name: "500 上游异常", err: upstreamError(http.StatusInternalServerError), want: false},
		{name: "网络错误", err: errors.New("dial tcp: connection refused"), want: false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, got := deterministicClientStatus(testCase.err)
			if got != testCase.want {
				t.Fatalf("确定性判定应为 %v, 实际 %v", testCase.want, got)
			}
			if got && status != testCase.status {
				t.Fatalf("状态码应为 %d, 实际 %d", testCase.status, status)
			}
		})
	}
}

// TestRetryAfterOf 验证 429 的重试间隔解析, 其余错误不产生冷却时长。
func TestRetryAfterOf(t *testing.T) {
	if duration := retryAfterOf(upstreamError(http.StatusTooManyRequests)); duration <= 0 {
		t.Fatalf("429 应给出默认重试间隔, 实际 %s", duration)
	}
	if duration := retryAfterOf(upstreamError(http.StatusBadRequest)); duration != 0 {
		t.Fatalf("非 429 不应给出重试间隔, 实际 %s", duration)
	}
}

// upstreamError 构造与真实转发路径同形状的上游错误: 库的 httpclient.Error 再包一层正文。
func upstreamError(status int) error {
	return fmt.Errorf("%w: upstream body", &httpclient.Error{
		Method:     http.MethodPost,
		URL:        "https://example.com/v1/chat/completions",
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       []byte("upstream body"),
	})
}
