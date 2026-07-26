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
  history: {
    maxItems: number;
    retention: Duration;
  };
  observability: {
    errorDetails: "off" | "safe";
    maxErrorDetail: string;
  };
  capture: {
    enabled: boolean;
    storageDir: string;
    retention: Duration;
    defaultRequestLimit: number;
    activationTimeout: Duration;
    maxBodySize: string;
    maxTotalSize: string;
    maxAttemptsPerRequest: number;
    minimumFreeDisk: string;
    logMaxItems: number;
    logRetention: Duration;
  };
  localization: {
    defaultLocale: "zh-CN" | "en-US";
    fallbackLocale: "zh-CN" | "en-US";
  };
  risk: {
    warningAfter: Duration;
    warningAttempts: number;
    authErrorAttempts: number;
    queueWarningPercent: number;
    minimumFreeDisk: string;
  };
  notifications: {
    stalledAfter: Duration;
    notifyOnRecovery: boolean;
    webhookUrl: string;
    deliveryAttempts: number;
    deliveryBackoff: Duration;
    eventTypes: string[];
    locale: "zh-CN" | "en-US";
  };
  logging: {
    level: string;
    locale: "zh-CN" | "en-US";
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
  lastErrorCode?: string;
  lastErrorDetails?: Record<string, unknown>;
}

export interface Status {
  paused: boolean;
  active: number;
  queued: number;
  waiting: number;
  requesting: number;
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

export interface TimelineEvent {
  time: string;
  type: string;
  attempt?: number;
  statusCode?: number;
  category?: string;
  message: string;
  messageCode?: string;
  messageDetails?: Record<string, unknown>;
  errorDetail?: ErrorDetail;
  waitMilliseconds?: number;
}

export interface ErrorDetail {
  message?: string;
  type?: string;
  code?: string;
  upstreamRequestId?: string;
  retryAfter?: string;
  responseBytes?: number;
  parsed: boolean;
}

export interface HistoryRecord {
  id: string;
  method: string;
  path: string;
  state: "successful" | "failed" | "canceled" | string;
  attempt: number;
  startedAt: string;
  completedAt?: string;
  lastError?: string;
  lastErrorCode?: string;
  lastErrorDetails?: Record<string, unknown>;
  lastErrorDetail?: ErrorDetail;
  events: TimelineEvent[];
}

export interface Alert {
  id: string;
  type: string;
  severity: string;
  requestId?: string;
  message: string;
  messageCode?: string;
  messageDetails?: Record<string, unknown>;
  createdAt: string;
  resolvedAt?: string;
}

export interface DiagnosticCheck {
  id: string;
  name: string;
  status: "pass" | "warn" | "fail";
  message: string;
  nameCode?: string;
  messageCode?: string;
  messageDetails?: Record<string, unknown>;
}

export interface DiagnosticReport {
  generatedAt: string;
  version: string;
  uptime: string;
  uptimeSeconds: number;
  healthy: boolean;
  checks: DiagnosticCheck[];
}

export interface RuntimeLogEntry {
  id: number;
  time: string;
  level: "debug" | "info" | "warn" | "error" | string;
  event: string;
  message: string;
  requestId?: string;
  attempt?: number;
  statusCode?: number;
  fields?: Record<string, unknown>;
}

export interface CaptureStatus {
  available: boolean;
  unavailableReason?: string;
  active: boolean;
  remainingRequests: number;
  deadline?: string;
  storageBytes: number;
  maxTotalBytes: number;
  captureCount: number;
}

export interface CaptureBodyPart {
  headers?: Record<string, string[]>;
  contentType?: string;
  originalBytes: number;
  storedBytes: number;
  truncated: boolean;
}

export interface CaptureAttempt {
  number: number;
  startedAt: string;
  finishedAt: string;
  statusCode?: number;
  error?: string;
  response?: CaptureBodyPart;
}

export interface CaptureRecord {
  id: string;
  requestId: string;
  method: string;
  path: string;
  state: string;
  startedAt: string;
  completedAt?: string;
  expiresAt: string;
  request: CaptureBodyPart;
  attempts: CaptureAttempt[];
  final?: CaptureBodyPart;
  capturedBytes: number;
  warnings?: string[];
}

export interface CapturePreviewPart {
  name: "request" | "attempt" | "final";
  attempt?: number;
  statusCode?: number;
  headers?: Record<string, string[]>;
  contentType?: string;
  body: string;
  originalBytes: number;
  truncated: boolean;
}

export interface CapturePreview {
  record: CaptureRecord;
  parts: CapturePreviewPart[];
}
