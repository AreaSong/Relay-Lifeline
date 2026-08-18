export type Duration = string;

export interface Config {
  schemaVersion: number;
  server: {
    listen: string;
    adminEnabled: boolean;
    configBackupDir: string;
    readHeaderTimeout: Duration;
    shutdownTimeout: Duration;
    maxRequestBody: string;
  };
  upstream: {
    baseUrl: string;
    connectTimeout: Duration;
    responseHeaderTimeout: Duration;
    responseBodyIdleTimeout: Duration;
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
  persistence: {
    enabled: boolean;
    directory: string;
    retention: Duration;
    syncWrites: boolean;
  };
  incidents: {
    enabled: boolean;
    correlationWindow: Duration;
    recoveryStableWindow: Duration;
    retention: Duration;
    maxItems: number;
  };
  lifecycle: {
    trackUncertainDelivery: boolean;
    preserveIdempotencyKey: boolean;
    generateIdempotencyKey: boolean;
    maxRequestDuration: Duration;
    clientDisconnectPolicy: "cancel" | "finish-attempt";
  };
  managementSecurity: {
    loginFailuresPerMinute: number;
    loginCooldown: Duration;
    sessionIdleTimeout: Duration;
  };
  metricsExport: {
    enabled: boolean;
    path: string;
  };
}

export interface RuntimeInfo {
  version: string;
  revision: string;
  builtAt: string;
  goVersion: string;
  platform: string;
  imageRef?: string;
  startedAt: string;
  uptimeSeconds: number;
  adminApiVersion: string;
  configSchemaVersion: number;
  process: {
    pid: number;
    parentPid: number;
    goroutines: number;
    cpuCount: number;
    gomaxprocs: number;
    heapAllocBytes: number;
    heapInuseBytes: number;
    stackInuseBytes: number;
    systemMemoryBytes: number;
    gcCycles: number;
    sampledAt: string;
  };
}

export interface SessionInfo {
  authenticated: boolean;
  role: "viewer" | "operator" | "sensitive";
  capabilities: Array<"view" | "operate" | "sensitive">;
  csrfToken?: string;
}

export interface ConfigChangePlan {
  schemaVersion: number;
  changedSections: string[];
  hotReloadSections: string[];
  restartSections: string[];
  restartRequired: boolean;
}

export interface ConfigSaveResult extends ConfigChangePlan {
  saved: boolean;
  backupPath?: string;
}

export interface RequestInfo {
  id: string;
  clientId?: string;
  taskId?: string;
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
  retryDeadline?: string;
  retryIntervalMilliseconds?: number;
  retryPolicy?: RetryPolicyInfo;
  actions?: RequestActions;
}

export type RetryScheduleMode = "inherit" | "immediate" | "fixed" | "random" | "exponential";

export interface RetryScheduleInfo {
  mode: RetryScheduleMode;
  intervalMilliseconds?: number;
  minimumIntervalMilliseconds?: number;
  maximumIntervalMilliseconds?: number;
  baseIntervalMilliseconds?: number;
}

export interface RetryPolicyInfo {
  state: "pending" | "active";
  durationMilliseconds: number;
  schedule: RetryScheduleInfo;
  maxAdditionalAttempts?: number;
  additionalAttemptsUsed: number;
  remainingAdditionalAttempts?: number;
  honorRetryAfter: boolean;
  appliedAt: string;
  activatedAt?: string;
  deadline?: string;
}

export interface RequestActions {
  canRetryNow: boolean;
  canSetRetryPolicy: boolean;
  retryRequiresConfirmation: boolean;
  canCancel: boolean;
}

export interface RetryPolicyInput {
  durationMilliseconds: number;
  schedule: {
    mode: RetryScheduleMode;
    intervalMilliseconds?: number;
    minimumIntervalMilliseconds?: number;
    maximumIntervalMilliseconds?: number;
    baseIntervalMilliseconds?: number;
  };
  maxAdditionalAttempts?: number;
  honorRetryAfter: boolean;
}

export interface BatchActionItem {
  id: string;
  outcome: "accepted" | "skipped";
  reason?: string;
  state?: string;
}

export interface BatchActionResponse {
  operationId: string;
  requested: number;
  accepted: number;
  skipped: number;
  triggered?: number;
  results: BatchActionItem[];
}

export type RepeatTaskState = "running" | "paused" | "stopped" | "expired" | "interrupted";

export interface RepeatTask {
  id: string;
  sourceRequestId: string;
  method: string;
  path: string;
  state: RepeatTaskState;
  idempotency: "preserve" | "regenerate";
  intervalMilliseconds: number;
  durationMilliseconds: number;
  startedAt: string;
  deadline?: string;
  nextRunAt?: string;
  lastRunAt?: string;
  stoppedAt?: string;
  executions: number;
  successes: number;
  failures: number;
  lastOutcome?: "successful" | "failed";
  lastStatusCode?: number;
  lastErrorCode?: string;
  lastExecutionId?: string;
  inFlight: boolean;
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
  attemptPhase?: "prepare" | "connect" | "request_write" | "response_headers" | "response_body" | "protocol" | "delivery" | string;
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
  clientId?: string;
  taskId?: string;
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
  eventsTruncated?: boolean;
  droppedEvents?: number;
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

export interface Incident {
  id: string;
  state: "open" | "recovering" | "resolved";
  startedAt: string;
  lastFailureAt: string;
  recoveryStartedAt?: string;
  resolvedAt?: string;
  affectedRequests: string[];
  failedAttempts: number;
  categories: Record<string, number>;
  statusCodes: Record<string, number>;
}

export interface RealtimeSnapshot {
  status: Status;
  alerts: Alert[];
  incidents: Incident[];
  metrics?: MetricsSnapshot;
  repeatTasks: RepeatTask[];
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
  clientId?: string;
  taskId?: string;
  attempt?: number;
  statusCode?: number;
  fields?: Record<string, unknown>;
}

export interface RuntimeLogPage {
  entries: RuntimeLogEntry[];
  nextAfter: number;
  oldestAfter: number;
  hasMore: boolean;
  hasGap: boolean;
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

export interface CaptureKeyStatus {
  activeId: string;
  configured: string[];
  recordsById: Record<string, number>;
  unresolved: number;
}

export interface CaptureKeyRewrapResult {
  activeId: string;
  updated: number;
  unchanged: number;
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

export type MetricsWindow = "15m" | "1h" | "6h" | "24h";

export interface MetricsPoint {
  time: string;
  requests: number;
  successful: number;
  failed: number;
  canceled: number;
  rejected: number;
  attempts: number;
  failedAttempts: number;
  recovered: number;
  active: number;
  queued: number;
  waiting: number;
  requesting: number;
}

export interface MetricBucket {
  bucket: string;
  count: number;
}

export interface MetricsSnapshot {
  generatedAt: string;
  dataSince: string;
  complete: boolean;
  window: MetricsWindow;
  from: string;
  to: string;
  totals: {
    requests: number;
    successful: number;
    failed: number;
    canceled: number;
    rejected: number;
    attempts: number;
    failedAttempts: number;
    recovered: number;
    successRate: number;
    averageRecoveryMilliseconds: number;
  };
  load: {
    active: number;
    queued: number;
    waiting: number;
    requesting: number;
  };
  series: MetricsPoint[];
  recovery: {
    durationBuckets: MetricBucket[];
    attemptBuckets: MetricBucket[];
  };
}

export interface MetricsErrors {
  generatedAt?: string;
  window: MetricsWindow;
  from: string;
  to: string;
  categories: Array<{ code: string; count: number }>;
}

export interface MonitoringEvent {
  id: number;
  time: string;
  code: string;
  category?: string;
  requestId?: string;
  statusCode?: number;
  attempt?: number;
}

export interface MonitoringEvents {
  events: MonitoringEvent[];
  nextAfter: number;
  oldestAfter?: number;
  hasMore: boolean;
  hasGap?: boolean;
}
