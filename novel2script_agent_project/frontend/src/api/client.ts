import type {
  ArtifactUpdateRequest,
  ArtifactUpdateResponse,
  ApprovalResolveRequest,
  ApprovalResolveResponse,
  CreateProjectRequest,
  CreateProjectResponse,
  HealthResponse,
  Message,
  Project,
  ProjectFileRequest,
  ProjectFileResponse,
  ProjectMessageRequest,
  ProjectMessageResponse,
  RunSnapshotResponse,
  RunActionResponse,
} from "./types";
import { ApiError, toApiError } from "./errors";

export const API_BASE = (import.meta.env.VITE_API_BASE || "http://127.0.0.1:8831").replace(/\/$/, "");

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const method = (options.method || "GET").toUpperCase();
  const maxAttempts = method === "GET" ? 3 : 1;
  let lastError: unknown;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    const controller = new AbortController();
    // Message POSTs may include one control-model call. Let the backend return its
    // structured timeout result instead of aborting at the same boundary.
    const timeoutMs = method === "GET" ? 30000 : 70000;
    const timeout = window.setTimeout(() => controller.abort(new DOMException("请求超时", "TimeoutError")), timeoutMs);
    const abortFromCaller = () => controller.abort(options.signal?.reason);
    options.signal?.addEventListener("abort", abortFromCaller, { once: true });
    try {
      const response = await fetch(`${API_BASE}${path}`, {
        ...options,
        headers: { "Content-Type": "application/json; charset=utf-8", "X-Request-ID": crypto.randomUUID(), ...(options.headers || {}) },
        signal: controller.signal,
      });
      const payload = response.status === 204 ? {} : await response.json().catch(() => ({}));
      if (!response.ok) {
        const error = toApiError(payload, `HTTP ${response.status}`, response.status);
        if (attempt + 1 < maxAttempts && response.status >= 500) { lastError = error; await retryDelay(attempt); continue; }
        throw error;
      }
      return payload as T;
    } catch (error) {
      lastError = error;
      if (options.signal?.aborted || attempt + 1 >= maxAttempts || (error instanceof ApiError && !error.retryable && (error.status || 0) < 500)) throw error;
      await retryDelay(attempt);
    } finally {
      window.clearTimeout(timeout);
      options.signal?.removeEventListener("abort", abortFromCaller);
    }
  }
  throw lastError;
}

function retryDelay(attempt: number) { return new Promise((resolve) => window.setTimeout(resolve, 250 * 2 ** attempt)); }

export async function health() {
  return request<HealthResponse>("/healthz");
}

export async function listProjects() {
  return request<{ projects: Project[] }>("/api/projects");
}

export async function createProject(input: CreateProjectRequest = {}) {
  return request<CreateProjectResponse>("/api/projects", {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export async function getProject(projectID: string) {
	return request<{ project: Project }>(`/api/projects/${encodeURIComponent(projectID)}`);
}

export async function deleteProject(projectID: string) {
  return request<{ deleted: boolean; project_id: string }>(`/api/projects/${encodeURIComponent(projectID)}`, {
    method: "DELETE",
  });
}

export async function listProjectMessages(projectID: string) {
  return request<{ messages: Message[] }>(`/api/projects/${encodeURIComponent(projectID)}/messages`);
}

export async function listProjectFiles(projectID: string) {
  return request<{ files: ProjectFileResponse["file"][] }>(`/api/projects/${encodeURIComponent(projectID)}/files`);
}

export async function uploadProjectFile(projectID: string, input: ProjectFileRequest) {
  return request<ProjectFileResponse>(`/api/projects/${encodeURIComponent(projectID)}/files`, {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export async function deleteProjectFile(fileID: string) {
  return request<{ deleted: boolean; file_id: string }>(`/api/files/${encodeURIComponent(fileID)}`, {
    method: "DELETE",
  });
}

export async function sendProjectMessage(projectID: string, input: ProjectMessageRequest) {
  return request<ProjectMessageResponse>(`/api/projects/${encodeURIComponent(projectID)}/messages`, {
    method: "POST",
    body: JSON.stringify(input),
  });
}

export async function getRunSnapshot(runID: string) {
  return request<RunSnapshotResponse>(`/api/runs/${encodeURIComponent(runID)}`);
}

export async function pauseRun(runID: string, reason = "用户暂停") {
  return request<RunActionResponse>(`/api/runs/${encodeURIComponent(runID)}/pause`, { method: "POST", body: JSON.stringify({ reason }) });
}

export async function resumeRun(runID: string) {
  return request<RunActionResponse>(`/api/runs/${encodeURIComponent(runID)}/resume`, { method: "POST", body: "{}" });
}

export async function updateArtifact(artifactID: string, input: ArtifactUpdateRequest) {
  return request<ArtifactUpdateResponse>(`/api/artifacts/${encodeURIComponent(artifactID)}`, {
    method: "PATCH",
    body: JSON.stringify(input),
  });
}

export async function resolveApproval(approvalRequestID: string, input: ApprovalResolveRequest) {
  return request<ApprovalResolveResponse>(`/api/approvals/${encodeURIComponent(approvalRequestID)}/resolve`, {
    method: "POST",
    body: JSON.stringify(input),
  });
}
