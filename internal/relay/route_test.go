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

// TestRouteWaitSecondsFollowsEarliestCooldown 验证没有可选成员时等到最早冷却到期, 而不是按重试间隔反复空转。
func TestRouteWaitSecondsFollowsEarliestCooldown(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2)
	// 成员 1 冷却 8 秒, 成员 2 冷却 30 秒: 应等 8 秒那个先恢复。
	recordRouteFailure(group, 1, group.RelayConfig.MemberMaxAttempts, 8*time.Second)
	recordRouteFailure(group, 2, group.RelayConfig.MemberMaxAttempts, 30*time.Second)

	seconds := routeWaitSeconds(group, nil)
	if seconds < 6 || seconds > 8 {
		t.Fatalf("等待秒数 = %d, 期望约 8", seconds)
	}
}

// TestRouteWaitSecondsCapsAtLimit 验证长冷却不会让请求一睡到底, 配置变化仍需在 10 秒内生效。
func TestRouteWaitSecondsCapsAtLimit(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1)
	recordRouteFailure(group, 1, group.RelayConfig.MemberMaxAttempts, 5*time.Minute)

	if seconds := routeWaitSeconds(group, nil); seconds != int(cooldownWaitLimit/time.Second) {
		t.Fatalf("等待秒数 = %d, 期望 %d", seconds, int(cooldownWaitLimit/time.Second))
	}
}

// TestRouteWaitSecondsFallsBackToRetryInterval 验证没有成员在冷却时退回配置的重试间隔。
func TestRouteWaitSecondsFallsBackToRetryInterval(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1)
	if seconds := routeWaitSeconds(group, nil); seconds != group.RelayConfig.MemberRetryIntervalSeconds {
		t.Fatalf("等待秒数 = %d, 期望 %d", seconds, group.RelayConfig.MemberRetryIntervalSeconds)
	}
}

// TestRouteWaitSecondsIgnoresExcludedMembers 验证本请求已排除的成员不参与最早冷却计算。
func TestRouteWaitSecondsIgnoresExcludedMembers(t *testing.T) {
	group := newTestGroup(t, model.GroupModeRoundRobin, 1, 2)
	recordRouteFailure(group, 1, group.RelayConfig.MemberMaxAttempts, 3*time.Second)
	recordRouteFailure(group, 2, group.RelayConfig.MemberMaxAttempts, 7*time.Second)

	// 成员 1 已被本请求排除, 只该看成员 2 的 7 秒。
	seconds := routeWaitSeconds(group, map[int]bool{1: true})
	if seconds < 5 || seconds > 7 {
		t.Fatalf("等待秒数 = %d, 期望约 7", seconds)
	}
}

// TestDeterministicCooldownUsesFloor 验证确定性 4xx 的冷却不低于 5 分钟, 避免每个冷却周期都被撞一次。
func TestDeterministicCooldownUsesFloor(t *testing.T) {
	group := testGroup(model.GroupModeRoundRobin, 1)
	group.RelayConfig.MemberCooldownSeconds = 60

	if cooldown := deterministicCooldown(group); cooldown != deterministicCooldownFloor {
		t.Fatalf("冷却 = %s, 期望 %s", cooldown, deterministicCooldownFloor)
	}
}

// TestDeterministicCooldownKeepsLargerConfig 验证配置的冷却比下限更长时以配置为准。
func TestDeterministicCooldownKeepsLargerConfig(t *testing.T) {
	group := testGroup(model.GroupModeRoundRobin, 1)
	group.RelayConfig.MemberCooldownSeconds = 900

	if cooldown := deterministicCooldown(group); cooldown != 15*time.Minute {
		t.Fatalf("冷却 = %s, 期望 15m", cooldown)
	}
}

// newLimitedGroup 构造带并发上限的分组并重置路由状态; limits 与 ids 一一对应, 0 表示不限。
func newLimitedGroup(t *testing.T, mode model.GroupMode, ids, limits []int) model.Group {
	t.Helper()
	ResetRouteState(1)
	t.Cleanup(func() { ResetRouteState(1) })

	group := testGroup(mode, ids...)
	for index := range group.Items {
		group.Items[index].MaxConcurrency = limits[index]
	}
	return group
}

// pickAndRelease 选一次并立即归还槽位, 用于只关心分配比例而不关心占用的用例。
func pickAndRelease(group model.Group) int {
	item := pickGroupItem(group, nil)
	ReleaseItemSlot(group.ID, item.ID)
	return item.ID
}

// TestPickWeightedDistributesByConcurrency 验证加权轮询按并发上限的比例分配请求。
func TestPickWeightedDistributesByConcurrency(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2, 3}, []int{4, 2, 1})

	counts := map[int]int{}
	for range 70 {
		id := pickAndRelease(group)
		if id == 0 {
			t.Fatalf("全部成员有余量时不应返回零值")
		}
		counts[id]++
	}

	want := map[int]int{1: 40, 2: 20, 3: 10}
	for id, expected := range want {
		if counts[id] != expected {
			t.Fatalf("成员 %d 分到 %d 次, 期望 %d 次 (实际分布 %v)", id, counts[id], expected, counts)
		}
	}
}

// TestPickWeightedUnlimitedSharesMaxWeight 验证未配置上限的成员按组内最大上限参与分配。
func TestPickWeightedUnlimitedSharesMaxWeight(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2}, []int{8, 0})

	counts := map[int]int{}
	for range 20 {
		counts[pickAndRelease(group)]++
	}
	if counts[1] != 10 || counts[2] != 10 {
		t.Fatalf("不限成员应与最大上限成员等权, 实际分布 %v", counts)
	}
}

// TestPickWeightedAllUnlimitedFallsBackToRoundRobin 验证全组都没有上限时退化为等权轮转。
func TestPickWeightedAllUnlimitedFallsBackToRoundRobin(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2}, []int{0, 0})

	counts := map[int]int{}
	for range 10 {
		counts[pickAndRelease(group)]++
	}
	if counts[1] != 5 || counts[2] != 5 {
		t.Fatalf("等权时应各分一半, 实际分布 %v", counts)
	}
}

// TestPickWeightedSkipsFullMember 验证满员的成员不再被选中, 请求转给仍有余量的成员。
func TestPickWeightedSkipsFullMember(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2}, []int{1, 0})

	first := pickGroupItem(group, nil)
	if first.ID == 0 {
		t.Fatalf("首轮应有成员可选")
	}
	// 成员 1 上限为 1, 已占满; 后续请求必须全部落到成员 2。
	for range 5 {
		item := pickGroupItem(group, nil)
		if item.ID != 2 {
			t.Fatalf("成员 1 满员后应选成员 2, 实际选中 %d", item.ID)
		}
		ReleaseItemSlot(group.ID, item.ID)
	}
}

// TestPickReturnsZeroWhenAllMembersFull 验证全部成员满员时返回零值, 由调用方等待而不是超发。
func TestPickReturnsZeroWhenAllMembersFull(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeRoundRobin, []int{1, 2}, []int{1, 1})

	if item := pickGroupItem(group, nil); item.ID == 0 {
		t.Fatalf("首轮应有成员可选")
	}
	if item := pickGroupItem(group, nil); item.ID == 0 {
		t.Fatalf("第二个成员应还有余量")
	}
	if item := pickGroupItem(group, nil); item.ID != 0 {
		t.Fatalf("全部满员时应返回零值, 实际选中 %d", item.ID)
	}
}

// TestGroupSaturatedDetectsFullMembers 验证满员能被识别出来, 归还槽位后立即恢复。
// 这个判定决定调用方是走亚秒级轮询还是走冷却等待, 判错会把吞吐压下去。
func TestGroupSaturatedDetectsFullMembers(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2}, []int{1, 1})

	if groupSaturated(group, nil) {
		t.Fatalf("还有余量时不应判为满员")
	}
	if item := pickGroupItem(group, nil); item.ID == 0 {
		t.Fatalf("首轮应有成员可选")
	}
	if item := pickGroupItem(group, nil); item.ID == 0 {
		t.Fatalf("第二个成员应还有余量")
	}
	if !groupSaturated(group, nil) {
		t.Fatalf("全部成员占满后应判为满员")
	}
	ReleaseItemSlot(group.ID, 1)
	if groupSaturated(group, nil) {
		t.Fatalf("归还一个槽位后不应再判为满员")
	}
}

// TestGroupSaturatedIgnoresCoolingMembers 验证冷却中的成员不算"满员", 等待时长仍交给冷却到期时间决定。
func TestGroupSaturatedIgnoresCoolingMembers(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2}, []int{4, 4})
	pickAndRelease(group) // 先建立路由状态, 冷却表才有地方可写。
	for _, id := range []int{1, 2} {
		if !recordRouteFailure(group, id, group.RelayConfig.MemberMaxAttempts, 0) {
			t.Fatalf("成员 %d 达到尝试上限后应进入冷却", id)
		}
	}

	if groupSaturated(group, nil) {
		t.Fatalf("全部成员都在冷却时不应判为满员")
	}
}

// TestGroupSaturatedSkipsExcludedMembers 验证被排除的成员不计入满员判定。
func TestGroupSaturatedSkipsExcludedMembers(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2}, []int{1, 1})

	for range 2 {
		if item := pickGroupItem(group, nil); item.ID == 0 {
			t.Fatalf("两个成员各有一个槽位, 应都能选中")
		}
	}
	// 两个成员都已占满, 排除谁都是满员。
	if !groupSaturated(group, map[int]bool{2: true}) {
		t.Fatalf("排除成员 2 后成员 1 仍满员, 应判为满员")
	}
	// 成员 2 腾出槽位后再排除成员 1, 判定就该看到成员 2 的余量。
	ReleaseItemSlot(group.ID, 2)
	if groupSaturated(group, map[int]bool{1: true}) {
		t.Fatalf("成员 2 已有余量, 不应判为满员")
	}
}

// TestReleaseItemSlotReopensCapacity 验证归还槽位后成员重新可选。
func TestReleaseItemSlotReopensCapacity(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeRoundRobin, []int{1}, []int{1})

	if item := pickGroupItem(group, nil); item.ID != 1 {
		t.Fatalf("首轮应选中成员 1")
	}
	if item := pickGroupItem(group, nil); item.ID != 0 {
		t.Fatalf("满员时应返回零值")
	}
	ReleaseItemSlot(group.ID, 1)
	if item := pickGroupItem(group, nil); item.ID != 1 {
		t.Fatalf("归还后应重新可选")
	}
}

// TestReleaseItemSlotIgnoresUnusedMember 验证归还未占用的成员不会把计数压到零以下。
func TestReleaseItemSlotIgnoresUnusedMember(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1}, []int{1})

	ReleaseItemSlot(group.ID, 1)
	ReleaseItemSlot(group.ID, 1)
	if item := pickGroupItem(group, nil); item.ID != 1 {
		t.Fatalf("未占用的归还不应影响可选择性")
	}
	if item := pickGroupItem(group, nil); item.ID != 0 {
		t.Fatalf("上限 1 的成员被占用后应满员, 实际选中 %d", item.ID)
	}
}

// TestPickFailoverOverflowsToLowerPriority 验证故障转移在首选成员满员时把请求交给下一优先级。
func TestPickFailoverOverflowsToLowerPriority(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeFailover, []int{1, 2}, []int{1, 0})

	if item := pickGroupItem(group, nil); item.ID != 1 {
		t.Fatalf("首选成员应被选中, 实际 %d", item.ID)
	}
	if item := pickGroupItem(group, nil); item.ID != 2 {
		t.Fatalf("首选成员满员后应溢出到成员 2, 实际 %d", item.ID)
	}
}

// TestPickWeightedSkipsCoolingMember 验证加权轮询同样跳过冷却中的成员。
func TestPickWeightedSkipsCoolingMember(t *testing.T) {
	group := newLimitedGroup(t, model.GroupModeWeighted, []int{1, 2}, []int{4, 4})
	pickAndRelease(group) // 先走一次选路建立路由状态, 冷却表才有地方可写。
	if !recordRouteFailure(group, 2, group.RelayConfig.MemberMaxAttempts, 0) {
		t.Fatalf("成员 2 达到尝试上限后应进入冷却")
	}

	for range 8 {
		if id := pickAndRelease(group); id == 2 {
			t.Fatalf("冷却中的成员不应被选中")
		}
	}
}
