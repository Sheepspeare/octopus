import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, apiRequest } from './client';

/**
 * Setting 数据
 */
export interface Setting {
    key: string;
    value: string;
}

export const SettingKey = {
    ProxyURL: 'proxy_url',
    StatsSaveInterval: 'stats_save_interval',
    ModelInfoUpdateInterval: 'model_info_update_interval',
    CORSAllowOrigins: 'cors_allow_origins',
    ModelFilter: 'model_filter',
    LogRequestBody: 'log_request_body',
} as const;

/**
 * 获取 Setting 列表 Hook
 * 
 * @example
 * const { data: settings, isLoading, error } = useSettingList();
 * 
 * if (isLoading) return <Loading />;
 * if (error) return <Error message={error.message} />;
 * 
 * settings?.forEach(setting => console.log(setting.key, setting.value));
 */
export function useSettingList() {
    return useQuery({
        queryKey: ['settings', 'list'],
        queryFn: () => apiRequest<Setting[]>('/api/v1/setting/list'),
        refetchInterval: 30000,
        refetchOnMount: 'always',
    });
}

/**
 * 设置 Setting Hook
 * 
 * @example
 * const setSetting = useSetSetting();
 * 
 * setSetting.mutate({
 *   key: 'theme',
 *   value: 'dark',
 * });
 */
export function useSetSetting() {
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: (data: Setting) =>
            apiRequest<Setting>('/api/v1/setting/set', { method: 'POST', body: data }),
        onSuccess: () => queryClient.invalidateQueries({ queryKey: ['settings', 'list'] }),
    });
}

/**
 * 重启服务 Hook
 *
 * 后端先按正常退出流程收尾（统计落库、日志正文落盘、关库），再以同一可执行文件原地重启。
 * 有在途请求时后端返回 409，需 force=true 才会强制重启。
 *
 * @example
 * const restart = useRestartService();
 * restart.mutate(false);
 */
export function useRestartService() {
    return useMutation({
        mutationFn: (force: boolean) =>
            apiRequest<null>(`/api/v1/setting/restart${force ? '?force=true' : ''}`, { method: 'POST' }),
    });
}

/**
 * 轮询等待服务恢复，超时返回 false。
 * 重启期间连接会被重置，此时 fetch 抛的是网络错误而非 ApiError，故只把「服务有响应」当作恢复。
 * 需要连续两次成功：旧进程在关闭过程中仍可能应答一次，只探一次容易误判成已恢复。
 */
export async function waitForServiceReady(timeoutMs = 30000): Promise<boolean> {
    const deadline = Date.now() + timeoutMs;
    let hits = 0;
    while (Date.now() < deadline) {
        await new Promise(resolve => setTimeout(resolve, 500));
        try {
            await apiRequest<string>('/api/v1/update/now-version');
            hits += 1;
            if (hits >= 2) return true;
        } catch (err) {
            if (err instanceof ApiError) {
                hits += 1;
                if (hits >= 2) return true;
            } else {
                hits = 0;
            }
        }
    }
    return false;
}

/**
 * 数据库导入/导出
 */
interface DBImportResult {
    rows_affected: Record<string, number>;
}

function parseFilename(contentDisposition: string | null): string | null {
    if (!contentDisposition) return null;
    // e.g. attachment; filename="octopus-export-20250101120000.json"
    const match = contentDisposition.match(/filename="([^"]+)"/i);
    return match?.[1] ?? null;
}

function exportFallbackFilename() {
    const d = new Date();
    const pad = (n: number) => String(n).padStart(2, '0');
    const ts = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
    return `octopus-export-${ts}.json`;
}

async function downloadBlob(blob: Blob, filename: string) {
    const url = URL.createObjectURL(blob);
    try {
        const a = document.createElement('a');
        a.href = url;
        a.download = filename;
        document.body.appendChild(a);
        a.click();
        a.remove();
    } finally {
        URL.revokeObjectURL(url);
    }
}

/**
 * 导出数据库（下载 JSON 文件）
 */
export function useExportDB() {
    return useMutation({
        mutationFn: async () => {
            const res = await fetch('./api/v1/setting/export', {
                method: 'GET',
                credentials: 'include',
            });

            if (!res.ok) {
                const data = await res.json().catch(() => null) as { message?: string } | null;
                throw new ApiError(res.status, data?.message || `Request failed: ${res.status}`);
            }

            const blob = await res.blob();
            const filename = parseFilename(res.headers.get('content-disposition')) || exportFallbackFilename();
            await downloadBlob(blob, filename);
            return { filename };
        },
    });
}

/**
 * 导入数据库（上传 JSON 文件）
 */
export function useImportDB() {
    return useMutation({
        mutationFn: async (file: File) => {
            const form = new FormData();
            form.append('file', file);

            const res = await fetch('./api/v1/setting/import', {
                method: 'POST',
                body: form,
                credentials: 'include',
            });

            const contentType = res.headers.get('content-type') || '';
            const isJson = contentType.includes('application/json');
            const data = isJson
                ? await res.json() as { message?: string; data?: DBImportResult }
                : await res.text();

            if (!res.ok) {
                const message = typeof data === 'string' ? data : data.message;
                throw new ApiError(res.status, message || `Request failed: ${res.status}`);
            }

            // 支持后端标准 ApiResponse：{code,message,data:{...}}
            return typeof data === 'string' ? data as unknown as DBImportResult : data.data as DBImportResult;
        },
    });
}
