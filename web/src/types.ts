export type Duration = string;

export interface Config {
  schemaVersion: number;
  server: {
    listen: string;
    adminEnabled: boolean;
    configBackupDir: string;
	  readHeaderTimeout: Duration;
	  readBodyTimeout: Duration;
	  idleTimeout: Duration;
	  downstreamWriteIdleTimeout: Duration;
	  shutdownTimeout: Duration;
	  maxHeaderBytes: number;
    maxRequestBody: string;
  };
  upstream: {
    baseUrl: string;
    connectTimeout: Duration;
    responseHeaderTimeout: Duration;
    responseBodyIdleTimeout: Duration;
  };
		upstreams: {
		strategy: "primary-only" | "weighted-priority";
			targets: Array<{ id: string; baseUrl: string; priority: number; weight: number; maxActive: number; idempotencyDomain: string; costMicrosPer1K: number; capabilityScore: number }>;
		health: { mode: "" | "passive" };
		circuit: { enabled: boolean; minimumRequests: number; failurePercent: number; openDuration: Duration; halfOpenMax: number };
	};
	egress: {
		denyPrivateNetworks: boolean;
		allowedHosts: string[];
	};
  retry: {
    enabled: boolean;
    mode: "all-errors" | "transient-errors";
    minInterval: Duration;
    maxInterval: Duration;
	    maxAttempts: number;
	    maxElapsed: Duration;
	    retryAfterCap: Duration;
    honorRetryAfter: boolean;
  };
  stream: {
    heartbeatInterval: Duration;
    memoryLimit: string;
    maxResponseBody: string;
    maxTotalCache: string;
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
		telemetry: {
			enabled: boolean;
			protocol: "grpc" | "http/protobuf" | "stdout";
			endpoint: string;
			insecure: boolean;
			sampleRatio: number;
			serviceName: string;
			environment: string;
			exportTimeout: Duration;
			metricInterval: Duration;
		};
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
		allowUncertainRetry: boolean;
		allowCrossDomainFailover: boolean;
    uncertainResolutionTarget: Duration;
    maxRequestDuration: Duration;
    clientDisconnectPolicy: "cancel" | "finish-attempt";
  };
  managementSecurity: {
		localAccessEnabled: boolean;
	    loginFailuresPerMinute: number;
    loginCooldown: Duration;
	    sessionIdleTimeout: Duration;
		sessionMaxLifetime: Duration;
		oidc: {
			enabled: boolean;
			issuerUrl: string;
			clientId: string;
			redirectUrl: string;
			scopes: string[];
			signingAlgorithms: string[];
			roleClaim: string;
			viewerValues: string[];
			operatorValues: string[];
			sensitiveValues: string[];
		};
  };
  metricsExport: {
    enabled: boolean;
    path: string;
  };
		governance: {
			mode: "observe" | "enforce";
			unknownUsagePolicy: "observe" | "deny";
			maxConcurrent: number;
		requestsPerMinute: number;
		tokenLimit: number;
		costLimitMicros: number;
		 tokenReservation: number;
		costReservationMicros: number;
		reservationMinTokens: number;
		reservationMaxTokens: number;
		reservationMinCostMicros: number;
		reservationMaxCostMicros: number;
		softThresholdPercent: number;
		forecastWindow: Duration;
		budgets: GovernanceBudgetConfig[];
		prices: Array<{ model: string; inputMicrosPerToken: number; outputMicrosPerToken: number }>;
	};
	slo: {
		enabled: boolean;
		availabilityTarget: number;
		recoveryLatencyTarget: Duration;
		window: Duration;
	};
	trafficPolicy: {
		enabled: boolean;
		mode: "observe" | "enforce";
		releaseStage: "draft" | "shadow" | "canary" | "full";
		canaryPercent: number;
		revision?: string;
		rules: TrafficPolicyRule[];
		shadow: {
			enabled: boolean;
			targetId: string;
			samplePercent: number;
			maxConcurrent: number;
			maxRequestBody: string;
			requestBudgetPerHour: number;
			costBudgetMicrosPerHour: number;
			costReservationMicros: number;
			requireIdempotency: boolean;
		};
		adaptive: {
			enabled: boolean;
			errorBudgetFloor: number;
			minimumObservations: number;
			maximumLatencyMilliseconds: number;
			latencyWeight: number;
			errorRateWeight: number;
			costWeight: number;
			capabilityWeight: number;
			switchCooldown: Duration;
			fallbackTargetId: string;
			autoStopBurnRate: number;
			autoStopFailureRate: number;
		};
	};
}

export interface TrafficPolicyRule {
	id: string;
	enabled: boolean;
	priority: number;
	method: string;
	pathPrefix: string;
	model: string;
	principalPrefix: string;
	action: "route" | "deny";
	targetId: string;
}

export interface PolicyInput {
	method: string;
	path: string;
	model: string;
	principal: string;
	requestId?: string;
	idempotencyKey?: string;
	bodyBytes?: number;
	sloHealthy?: boolean;
	errorBudgetRemaining?: number;
	errorBudgetBurnRate?: number;
	targets?: Array<{ id: string; circuitState: string; observations: number; latencyMilliseconds: number; errorRate?: number; rateLimitRate?: number; costMicrosPer1K?: number; capabilityScore?: number }>;
}

export interface PolicyDecision {
	id: number;
	evaluatedAt: string;
	dryRun: boolean;
	enabled: boolean;
	mode: string;
	matchedRuleId?: string;
	action: string;
	targetId?: string;
	denied: boolean;
	enforced: boolean;
	reason: string;
	adaptive: boolean;
	shadowTargetId?: string;
	shadowEligible: boolean;
	canarySelected: boolean;
	fallback: boolean;
	adaptiveScore?: number;
	explanation?: string[];
}

export interface PolicyStatus {
	enabled: boolean;
	mode: string;
	rules: number;
	decisions: number;
	denied: number;
	routed: number;
	adaptive: number;
	shadowPlanned: number;
	shadowActive: number;
	shadowSent: number;
	shadowSkipped: number;
	shadowFailed: number;
	shadowReservedCostMicros: number;
	adaptiveStopped: boolean;
	adaptiveStopReason?: string;
	adaptiveSwitches: number;
	adaptiveLastTargetId?: string;
	adaptiveLastScore?: number;
	recent: PolicyDecision[];
}

export interface PolicyReleaseRecord {
	revision: string;
	stage: "shadow" | "canary" | "full";
	canaryPercent: number;
	createdAt: string;
	actor?: string;
	policy: Config["trafficPolicy"];
}

export interface PolicyReleaseStatus {
	currentRevision: string;
	currentStage: string;
	draftRevision?: string;
	draft?: Config["trafficPolicy"];
	history: PolicyReleaseRecord[];
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
	authMethod?: "local" | "oidc" | "bearer";
}

export interface LoginOptions {
	localEnabled: boolean;
	oidc: { enabled: boolean; available: boolean };
}

export interface ConfigChangePlan {
  schemaVersion: number;
  changedSections: string[];
  hotReloadSections: string[];
  restartSections: string[];
  restartRequired: boolean;
	fields?: Array<{ path: string; applyMode: "hot" | "restart" }>;
}

export interface ConfigSaveResult extends ConfigChangePlan {
  saved: boolean;
  backupPath?: string;
	activeRevision: string;
	desiredRevision: string;
}

export interface ConfigRuntimeState {
	active: Config;
	desired: Config;
	activeRevision: string;
	desiredRevision: string;
	pendingRestart: ConfigChangePlan;
}

export interface ConfigVersion {
	name: string;
	modifiedAt: string;
	sizeBytes: number;
	sha256?: string;
	schemaVersion?: number;
	valid: boolean;
	error?: string;
	diff: ConfigChangePlan;
	applyPlan: ConfigChangePlan;
}

export interface UpstreamPoolStatus {
	strategy: string;
	targets: Array<{
		target: Config["upstreams"]["targets"][number];
		circuitState: "closed" | "open" | "half-open";
		active: number;
		failureCount: number;
		successCount: number;
		lastFailureAt?: string;
		lastSuccessAt?: string;
		lastLatencyMilliseconds?: number;
		lastErrorClass?: string;
			halfOpenLeases?: number;
			errorRate: number;
			rateLimitRate: number;
	}>;
}

export interface GovernanceStatus {
	mode: string;
	unknownUsagePolicy: string;
	principals: number;
	reservations: number;
	entries: Array<{ scope: string; key: string; principal: string; windowStarted: string; requests: number; active: number; tokens: number; costMicros: number; reservedTokens: number; reservedCostMicros: number; unknownUsage: number }>;
	softThreshold: boolean;
	estimatedExhaustionMinutes?: number;
	counters: {
		admitted: number;
		rejected: Record<string, number>;
		settlements: number;
		knownSettlements: number;
		unknownSettlements: number;
		reconciled: number;
		persistenceFailures: number;
	};
	ledger: {
		enabled: boolean;
		healthy: boolean;
		state?: string;
		failedAt?: string;
		failedStage?: string;
		failureCount?: number;
	};
}

export interface GovernanceBudgetConfig {
	scope: "principal" | "tenant" | "model" | "upstream" | string;
	key: string;
	maxConcurrent: number;
	requestsPerMinute: number;
	tokenLimit: number;
	costLimitMicros: number;
}

export interface TelemetryStatus {
	enabled: boolean;
	protocol?: string;
	healthy: boolean;
	traceHealthy: boolean;
	metricHealthy: boolean;
	traceExportFailures: number;
	metricExportFailures: number;
	lastSuccessAt?: string;
	lastFailureAt?: string;
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
	persistenceDegraded?: boolean;
	persistencePending?: boolean;
	uncertainSince?: string;
	uncertainResolution?: string;
	uncertainResolvedAt?: string;
}

/** Actions that settle an upstream attempt whose delivery result is unknown. */
export type UncertainResolutionAction = "confirm_success" | "abandon" | "request_compensation";

export interface UncertainAttemptEvidence {
  attempt: number;
  targetId?: string;
  targetDomain?: string;
  statusCode?: number;
  category?: string;
  attemptPhase?: string;
  wroteRequest: boolean;
  idempotencyKeyHash?: string;
  requestBytes?: number;
  latencyMilliseconds?: number;
  upstreamRequestId?: string;
}

export interface UncertainEvidence {
  requestId: string;
  method: string;
  path: string;
  state: string;
  attempt: number;
  startedAt: string;
  uncertainSince: string;
  attempts: UncertainAttemptEvidence[];
}

export interface UncertainPreview {
  confirmationToken: string;
  expiresAt: string;
  evidence: UncertainEvidence;
}

export interface UncertainResolutionInput {
  action: UncertainResolutionAction;
  confirmationToken: string;
  reason: string;
}

export interface UncertainResolutionResponse {
  accepted: boolean;
  action: UncertainResolutionAction;
  result?: {
    id?: string;
    outcome?: string;
    reason?: string;
    state?: string;
  };
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
  canRepeat: boolean;
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
	maxExecutions?: number;
	maxFailures?: number;
	failureThreshold?: number;
	consecutiveFailures?: number;
	circuitOpen?: boolean;
	maxTokens?: number;
	tokensUsed?: number;
	lastUsageTokens?: number;
	tokenUsageMissing?: boolean;
	stopReason?: "deadline" | "max_executions" | "max_failures" | "failure_circuit_open" | string;
	executionAudit?: Array<{
		id: string;
		startedAt: string;
		completedAt: string;
		success: boolean;
		statusCode?: number;
		errorCode?: string;
		durationMilliseconds: number;
		usageTokens?: number;
		usageAvailable: boolean;
	}>;
}

export interface Status {
  paused: boolean;
  mode: "running" | "paused" | "draining" | "maintenance" | string;
  active: number;
  queued: number;
  waiting: number;
  requesting: number;
  totalRequests: number;
  successful: number;
  failedAttempts: number;
	persistenceDegraded?: boolean;
	persistencePending?: number;
  upstream: {
    state: "unknown" | "healthy" | "degraded";
    lastChecked?: string;
    lastError?: string;
  };
  requests: RequestInfo[];
}

export interface HealthComponent {
  name: string;
  state: string;
  healthy: boolean;
  details?: Record<string, unknown>;
}

export interface HealthSummary {
  generatedAt: string;
  overall: "healthy" | "degraded" | string;
  components: HealthComponent[];
  actions?: string[];
}

export interface SLOSnapshot {
	window: string; availability: number; availabilityTarget: number;
	recoveryLatencyMilliseconds: number; recoveryLatencyTargetMilliseconds: number;
	errorBudget: number; errorBudgetRemaining: number; burnRate: number; healthy: boolean;
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

export interface HistoryPage {
	items: HistoryRecord[];
	nextCursor?: string;
	hasMore: boolean;
}

export interface ListFilters {
	q: string;
	state: string;
	from: string;
	to: string;
}

export interface IncidentPage {
	items: Incident[];
	nextCursor?: string;
	hasMore: boolean;
}

export interface IncidentDetail {
	incident: Incident;
	requests: HistoryRecord[];
	timeline: Array<{
		time: string;
		type: string;
		requestId?: string;
		attempt?: number;
		statusCode?: number;
		category?: string;
		message: string;
		waitMilliseconds?: number;
		attemptPhase?: string;
	}>;
	affectedRequestsTruncated: boolean;
}

export interface RealtimeSnapshot {
  status: Status;
  alerts: Alert[];
  incidents: Incident[];
  metrics?: MetricsSnapshot;
  repeatTasks: RepeatTask[];
}

export interface RealtimeEvent {
	version: 1;
	sequence: number;
	type: "sync" | "reset" | "status" | "alerts" | "incidents" | "metrics" | "repeat_tasks";
	generatedAt: string;
	data: unknown;
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
	persistenceHealthy: boolean;
	failureCount?: number;
	failedStage?: string;
	lastFailureAt?: string;
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
  failovers: number;
  uncertain: number;
  persistenceFailures: number;
  captureFailures: number;
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
    failovers: number;
    uncertain: number;
    persistenceFailures: number;
    captureFailures: number;
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

export interface NotificationStatus {
	configured: boolean;
	signingConfigured: boolean;
	signingKeyId?: string;
	queueDepth: number;
	queueCapacity: number;
	enqueued: number;
	delivered: number;
	failed: number;
	dropped: number;
	lastAttemptAt?: string;
	lastSuccessAt?: string;
	lastFailureAt?: string;
}

export interface NotificationDelivery {
	id: number;
	eventType: string;
	requestId?: string;
	outcome: "delivered" | "failed" | "dropped";
	attempts: number;
	statusCode?: number;
	completedAt: string;
}
