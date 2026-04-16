/**
 * API client for OmniRepo REST API.
 *
 * - Base URL: /api/v1/
 * - Cookie-based auth (credentials: 'include')
 * - 401 -> redirect to /login
 * - 503 with maintenance -> propagates for maintenance banner
 */
export class ApiError extends Error {
    status;
    code;
    detail;
    constructor(status, code, detail) {
        super(detail);
        this.status = status;
        this.code = code;
        this.detail = detail;
        this.name = 'ApiError';
    }
}
class ApiClient {
    baseUrl = '/api/v1';
    async get(path, params) {
        const url = new URL(this.baseUrl + path, window.location.origin);
        if (params) {
            Object.entries(params).forEach(([k, v]) => url.searchParams.set(k, v));
        }
        const res = await fetch(url.toString(), { credentials: 'include' });
        return this.handleResponse(res);
    }
    async post(path, body) {
        const res = await fetch(this.baseUrl + path, {
            method: 'POST',
            credentials: 'include',
            headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
            body: body !== undefined ? JSON.stringify(body) : undefined,
        });
        return this.handleResponse(res);
    }
    async patch(path, body) {
        const res = await fetch(this.baseUrl + path, {
            method: 'PATCH',
            credentials: 'include',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        return this.handleResponse(res);
    }
    async del(path) {
        const res = await fetch(this.baseUrl + path, {
            method: 'DELETE',
            credentials: 'include',
        });
        return this.handleResponse(res);
    }
    async upload(path, file, onProgress) {
        return new Promise((resolve, reject) => {
            const xhr = new XMLHttpRequest();
            xhr.open('PUT', this.baseUrl + path);
            xhr.withCredentials = true;
            if (onProgress) {
                xhr.upload.addEventListener('progress', (e) => {
                    if (e.lengthComputable) {
                        onProgress(Math.round((e.loaded / e.total) * 100));
                    }
                });
            }
            xhr.onload = () => {
                if (xhr.status === 401) {
                    window.location.href = '/login';
                    reject(new ApiError(401, 'unauthorized', 'Unauthorized'));
                    return;
                }
                if (xhr.status >= 200 && xhr.status < 300) {
                    resolve();
                }
                else {
                    try {
                        const err = JSON.parse(xhr.responseText);
                        reject(new ApiError(xhr.status, err.error ?? 'unknown', err.detail ?? xhr.statusText));
                    }
                    catch {
                        reject(new ApiError(xhr.status, 'unknown', xhr.statusText));
                    }
                }
            };
            xhr.onerror = () => reject(new Error('Network error'));
            xhr.send(file);
        });
    }
    async handleResponse(res) {
        if (res.status === 401) {
            window.location.href = '/login';
            throw new ApiError(401, 'unauthorized', 'Unauthorized');
        }
        if (!res.ok) {
            const err = await res.json().catch(() => ({
                error: 'unknown',
                detail: res.statusText,
            }));
            throw new ApiError(res.status, err.error ?? 'unknown', err.detail ?? res.statusText);
        }
        if (res.status === 204)
            return undefined;
        return res.json();
    }
}
export const api = new ApiClient();
