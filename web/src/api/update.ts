import { useQuery } from '@tanstack/react-query';
import { apiRequest } from './client';

// 本构建已禁用在线更新: 官方二进制会覆盖掉全部定制改动, 后端 /api/v1/update 也直接拒绝更新请求。
// 故这里只保留读取当前版本的接口, 不再提供查询最新发布与触发更新的封装。

/**
 * 获取后端当前版本 Hook
 *
 * 后端: GET /api/v1/update/now-version -> string
 */
export function useNowVersion() {
    return useQuery({
        queryKey: ['update', 'now-version'],
        queryFn: () => apiRequest<string>('/api/v1/update/now-version'),
        refetchInterval: 3600000, // 1 小时
        refetchOnMount: 'always',
    });
}
