// SPDX-License-Identifier: MIT
export interface Spread {
  median: number;
  min: number;
  max: number;
}
export interface BenchScenario {
  id: string;
  source: string;
  source_sha256: string;
  git_sha: string;
  created_utc: string;
  network: "loopback";
  protocol: "FXP";
  config: {
    mode: "open" | "closed";
    pacer: string;
    repetitions: number;
    pinning_notes: string;
  };
  environment: Record<string, string>;
  summary: Record<string, Spread>;
}
export interface BenchDocument {
  schema_version: 1;
  profile: string;
  scenarios: BenchScenario[];
}
const metrics = [
  "ack_p50_ns",
  "ack_p99_ns",
  "ack_after_send_p50_ns",
  "ack_after_send_p99_ns",
];
function record(v: unknown): v is Record<string, unknown> {
  return !!v && typeof v === "object" && !Array.isArray(v);
}
export function parseBench(value: unknown): BenchDocument {
  if (
    !record(value) ||
    value.schema_version !== 1 ||
    value.profile !== "/api/bench/profile-r4" ||
    !Array.isArray(value.scenarios) ||
    !value.scenarios.length
  )
    throw Error("Benchmark data unavailable or malformed");
  const ids = new Set<string>();
  for (const s of value.scenarios) {
    if (
      !record(s) ||
      typeof s.id !== "string" ||
      !s.id ||
      ids.has(s.id) ||
      s.network !== "loopback" ||
      s.protocol !== "FXP" ||
      typeof s.git_sha !== "string" ||
      !/^[a-f0-9]{40}$/.test(s.git_sha) ||
      !record(s.config) ||
      !["open", "closed"].includes(String(s.config.mode)) ||
      typeof s.config.pacer !== "string" ||
      !s.config.pacer ||
      typeof s.config.pinning_notes !== "string" ||
      !s.config.pinning_notes ||
      !Number.isInteger(s.config.repetitions) ||
      Number(s.config.repetitions) < 2 ||
      !record(s.environment) ||
      !record(s.summary)
    )
      throw Error("Benchmark data unavailable or malformed");
    if (
      s.source !== `benchmarks/round4/${s.id}.json.gz` ||
      typeof s.source_sha256 !== "string" ||
      !/^[a-f0-9]{64}$/.test(s.source_sha256) ||
      typeof s.created_utc !== "string" ||
      !Number.isFinite(Date.parse(s.created_utc))
    )
      throw Error("Missing artifact provenance");
    ids.add(s.id);
    for (const key of [
      "cpu",
      "os",
      "arch",
      "go",
      "rustc_local",
      "revision",
      "pinning_notes",
    ])
      if (typeof s.environment[key] !== "string" || !s.environment[key])
        throw Error("Missing environment metadata");
    if (s.environment.revision !== s.git_sha)
      throw Error("Build revision mismatch");
    for (const key of metrics) {
      const v = s.summary[key];
      if (
        !record(v) ||
        ![v.min, v.median, v.max].every(
          (n) => typeof n === "number" && Number.isFinite(n),
        ) ||
        Number(v.min) < 0 ||
        Number(v.min) > Number(v.median) ||
        Number(v.median) > Number(v.max)
      )
        throw Error("Invalid repetition spread");
    }
  }
  return value as unknown as BenchDocument;
}
export function chartRows(s: BenchScenario) {
  // A chart contains exactly one scenario/environment/protocol. Never pool distributions.
  return ["p50", "p99"].map((percentile) => {
    const intended = s.summary[`ack_${percentile}_ns`]!;
    const post = s.summary[`ack_after_send_${percentile}_ns`]!;
    return {
      percentile,
      intended: intended.median / 1e6,
      post: post.median / 1e6,
      intendedError: [
        (intended.median - intended.min) / 1e6,
        (intended.max - intended.median) / 1e6,
      ],
      postError: [
        (post.median - post.min) / 1e6,
        (post.max - post.median) / 1e6,
      ],
    };
  });
}
