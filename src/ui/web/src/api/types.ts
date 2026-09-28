// Wire types of the core's local API (see src/ipc/server.go, src/model,
// src/core). Field names match the Go JSON tags exactly.

export type SandboxState =
  | 'stopped'
  | 'preparing'
  | 'starting'
  | 'booting'
  | 'provisioning'
  | 'ready'
  | 'installing'
  | 'launching'
  | 'stopping'
  | 'recovering'
  | 'error';

export interface ErrorInfo {
  title: string;
  causes?: string[];
  details?: string;
  code?: string;
}

export interface AppState {
  package: string;
  label?: string;
  version?: string;
  file?: string;
  running: boolean;
}

export interface Status {
  state: SandboxState;
  message: string;
  runtime?: string;
  android?: string;
  accelerator?: string;
  accelerated: boolean;
  warnings?: string[];
  error?: ErrorInfo;
  captureActive: boolean;
  httpsInspection: boolean;
  caFingerprint?: string;
  sessionId?: string;
  app?: AppState;
  displayReady: boolean;
  since: string;
}

export interface RuntimeInfo {
  name: string;
  android: string;
  sdk: number;
  abis: string[];
  translation: boolean;
  description: string;
}

export interface GeneratorInfo {
  id: string;
  label: string;
}

export interface Info {
  version: string;
  runtimes: RuntimeInfo[] | null;
  generators: GeneratorInfo[] | null;
  config: Record<string, unknown>;
}

export interface Snapshot {
  tag: string;
  created: string;
  vmSize: string;
}

export type IssueSeverity = 'error' | 'warning';

export interface ApkIssue {
  severity: IssueSeverity;
  code: string;
  message: string;
}

export interface ApkInfo {
  size: number;
  package: string;
  versionCode: number;
  versionName?: string;
  minSdk: number;
  targetSdk: number;
  minSdkCodename?: string;
  label?: string;
  launchableActivity?: string;
  permissions?: string[];
  debuggable?: boolean;
  testOnly?: boolean;
  split?: string;
  abis?: string[];
  issues?: ApkIssue[];
}

export interface RuntimeDecision {
  runtime: string;
  translated: boolean;
  reason: string;
}

export interface APKEntry {
  id: string;
  fileName: string;
  size: number;
  uploaded: string;
  info?: ApkInfo;
  runtime: RuntimeDecision;
  valid: boolean;
  problems?: ApkIssue[];
}

export interface Session {
  id: string;
  number: number;
  name?: string;
  apk?: string;
  package?: string;
  runtime?: string;
  startedAt: string;
  endedAt?: string;
  saved: boolean;
  requests: number;
  domains: number;
  bytes: number;
}

export type EventKind = 'http' | 'websocket' | 'dns' | 'tls' | 'tcp' | 'udp';
export type Category =
  | 'api'
  | 'document'
  | 'image'
  | 'media'
  | 'script'
  | 'style'
  | 'font'
  | 'websocket'
  | 'dns'
  | 'other';
export type EventState = 'pending' | 'complete' | 'error';
export type Initiator = 'guest' | 'replay';

export interface Summary {
  id: string;
  seq: number;
  kind: EventKind;
  category: Category;
  state: EventState;
  initiator: Initiator;
  timestamp: string;
  durationMs: number;
  package?: string;
  protocol?: string;
  method?: string;
  scheme?: string;
  host?: string;
  port?: number;
  path?: string;
  query?: string;
  status?: number;
  mime?: string;
  requestSize: number;
  responseSize: number;
  encrypted?: boolean;
  error?: string;
}

export interface EventPage {
  rows: Summary[];
  total: number;
  all: number;
  version: number;
}

export interface Header {
  name: string;
  value: string;
}

export interface BodyRef {
  hash: string;
  size: number;
  stored: number;
  truncated?: boolean;
  encoding?: string;
  mime?: string;
}

export interface Timing {
  blocked: number;
  dns: number;
  connect: number;
  tls: number;
  send: number;
  wait: number;
  receive: number;
}

export interface CertInfo {
  subject: string;
  issuer: string;
  notBefore: string;
  notAfter: string;
  dnsNames?: string[];
  sha256: string;
}

export interface TLSInfo {
  sni?: string;
  version?: string;
  cipherSuite?: string;
  alpn?: string;
  clientAlpn?: string[];
  intercepted: boolean;
  passthroughReason?: string;
  serverCerts?: CertInfo[];
}

export interface ConnInfo {
  id: string;
  clientAddr: string;
  serverAddr: string;
  remoteAddr?: string;
  reused?: boolean;
  bytesUp: number;
  bytesDown: number;
}

export interface DNSAnswer {
  name: string;
  type: string;
  ttl: number;
  data: string;
}

export interface DNSInfo {
  question: string;
  qtype: string;
  rcode: string;
  answers?: DNSAnswer[];
}

export interface WSFrame {
  seq: number;
  time: string;
  outgoing: boolean;
  opcode: number;
  length: number;
  /** base64 (Go []byte) */
  data?: string;
  truncated?: boolean;
}

export type BodyKind = 'empty' | 'json' | 'xml' | 'html' | 'text' | 'image' | 'binary';

export interface QueryParam {
  name: string;
  value: string;
}

export interface EventDetail extends Summary {
  sessionId: string;
  replayOf?: string;
  statusText?: string;
  requestHeaders?: Header[];
  responseHeaders?: Header[];
  requestBody?: BodyRef;
  responseBody?: BodyRef;
  timing?: Timing;
  tls?: TLSInfo;
  conn?: ConnInfo;
  dns?: DNSInfo;
  frames?: WSFrame[];
  url?: string;
  queryParams?: QueryParam[];
  requestKind?: BodyKind;
  responseKind?: BodyKind;
  replayable: boolean;
}

export type QuickFilter = 'all' | 'api' | 'document' | 'image' | 'media' | 'websocket' | 'dns' | 'other';
export type SortKey = 'seq' | 'time' | 'method' | 'host' | 'path' | 'status' | 'type' | 'size' | 'duration';

export interface SessionStats {
  requests: number;
  domains: number;
  bytes: number;
}

export type WSMessage =
  | { type: 'status'; status: Status }
  | { type: 'events'; sessionId: string; stats?: SessionStats };

export interface Body {
  status: number;
  bytes: Uint8Array;
  kind: BodyKind;
  truncated: boolean;
  size: number;
  contentType: string;
  decodeWarning: string;
}
