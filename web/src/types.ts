export type Duration = string;

export interface Config {
  server: {
    listen: string;
    adminEnabled: boolean;
    readHeaderTimeout: Duration;
    shutdownTimeout: Duration;
    maxRequestBody: string;
  };
  upstream: {
    baseUrl: string;
    connectTimeout: Duration;
    responseHeaderTimeout: Duration;
  };
  retry: {
    enabled: boolean;
    mode: "all-errors" | "transient-errors";
    minInterval: Duration;
    maxInterval: Duration;
    maxAttempts: number;
    honorRetryAfter: boolean;
  };
  stream: {
    heartbeatInterval: Duration;
    memoryLimit: string;
    tempDir: string;
  };
  queue: {
    maxActive: number;
    maxWaiting: number;
    recoverySpacing: Duration;
  };
  notifications: {
    stalledAfter: Duration;
    notifyOnRecovery: boolean;
    webhookUrl: string;
  };
  logging: {
    level: string;
    logRequestBody: boolean;
    logResponseBody: boolean;
    logAuthorization: boolean;
  };
}

export interface RequestInfo {
  id: string;
  method: string;
  path: string;
  state: string;
  attempt: number;
  startedAt: string;
  updatedAt: string;
  nextRetryAt?: string;
  lastError?: string;
}

export interface Status {
  paused: boolean;
  active: number;
  totalRequests: number;
  successful: number;
  failedAttempts: number;
  upstream: {
    state: "unknown" | "healthy" | "degraded";
    lastChecked?: string;
    lastError?: string;
  };
  requests: RequestInfo[];
}
