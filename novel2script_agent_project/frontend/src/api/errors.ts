import type { Artifact, ErrorResponse } from "./types";

export class ApiError extends Error {
  code: string;
  recoverable: boolean;
  retryable: boolean;
  details: Record<string, unknown>;
  status?: number;

  constructor(error: ErrorResponse["error"], status?: number) {
    super(error.message);
    this.name = "ApiError";
    this.code = error.code;
    this.recoverable = Boolean(error.recoverable);
    this.retryable = Boolean(error.retryable);
    this.details = error.details || {};
    this.status = status;
  }
}

export function isErrorResponse(value: unknown): value is ErrorResponse {
  return Boolean(value && typeof value === "object" && "error" in value);
}

export function toApiError(value: unknown, fallbackMessage: string, status?: number) {
  if (isErrorResponse(value) && value.error?.message) return new ApiError(value.error, status);
  const error = new Error(fallbackMessage) as Error & { status?: number };
  error.status = status;
  return error;
}

export function currentArtifactFromConflict(error: unknown): Artifact | null {
  if (!(error instanceof ApiError) || error.status !== 409) return null;
  const value = error.details.current_artifact;
  if (!value || typeof value !== "object") return null;
  const artifact = value as Partial<Artifact>;
  return artifact.artifact_id && artifact.artifact_type && typeof artifact.version === "number" && artifact.payload
    ? (artifact as Artifact)
    : null;
}
