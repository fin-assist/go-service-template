/**
 * Optional contract test against the real, pinned Renovate package (Node 24).
 * Install renovate@44.176.2 with --ignore-scripts in a temporary npm prefix, then:
 * RENOVATE_RUNTIME=/path/to/prefix node --experimental-test-module-mocks scripts/test_renovate_runtime.mjs
 * The runner must impose a process timeout. No network or repository mutations occur.
 */
import assert from 'node:assert/strict';
import { mock } from 'node:test';
import { readFileSync, realpathSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';

assert.ok(process.env.RENOVATE_RUNTIME, 'Set RENOVATE_RUNTIME to the temporary npm prefix');
const pkg = realpathSync(resolve(process.env.RENOVATE_RUNTIME, 'node_modules/renovate'));
assert.equal(JSON.parse(readFileSync(resolve(pkg, 'package.json'))).version, '44.176.2');
const mod = (path) => pathToFileURL(resolve(pkg, 'dist', path)).href;

// Preserve the actual approval/cache gates. External rate counters and the
// closed-PR notifier are stubs; the latter would otherwise delete saved branches.
mock.module(mod('workers/global/limits.js'), { namedExports: {
  calcLimit: () => 0, counts: {}, getCount: () => 0, hasMultipleLimits: () => false,
  incCountValue() {}, incLimitedValue() {}, isLimitReached: () => false,
  setCount() {}, setMaxLimit() {},
} });
mock.module(mod('workers/repository/update/branch/handle-existing.js'), { namedExports: {
  handleClosedPr: async () => {}, handleModifiedPr: async () => {},
} });
const { init } = await import(mod('logger/index.js'));
await init();
const { getConfig } = await import(mod('config/defaults.js'));
const { branchifyUpgrades } = await import(mod('workers/repository/updates/branchify.js'));
const { processBranch } = await import(mod('workers/repository/update/branch/index.js'));
const { ensureDependencyDashboard, readDashboardBody } = await import(mod('workers/repository/dependency-dashboard.js'));
const { default: apis } = await import(mod('modules/platform/api.js'));
const { platformScmImpls } = await import(mod('modules/platform/scm.js'));
const { setPlatformApi } = await import(mod('modules/platform/index.js'));
const { GlobalConfig } = await import(mod('config/global.js'));

let existingBranches = new Set();
let closedBranch = null;
let dashboardBody = '';
let checkedBody = '';
apis.set('github', {
  getBranchPr: async () => null,
  findPr: async (query) => query.branchName === closedBranch ? { number: 1, state: 'closed' } : null,
  getPr: async () => ({ state: 'closed' }),
  ensureIssue: async ({ body }) => { dashboardBody = body; },
  findIssue: async () => ({ number: 2, body: checkedBody }),
  maxBodyLength: () => 60000,
  massageMarkdown: (body) => body,
});
platformScmImpls.set('github', class {
  async branchExists(name) { return existingBranches.has(name); }
  async isBranchModified() { return false; }
  async checkoutBranch() {
    // Stop exactly before checking out a branch or calculating file writes.
    const error = new Error('repository-changed');
    error.fixtureBoundary = true;
    throw error;
  }
});
setPlatformApi('github');
GlobalConfig.set({ dryRun: null });

function dependency(name, value, old = '3.0.0', type = 'major') {
  return { depName: name, packageName: name, datasource: 'npm', currentValue: old,
    currentVersion: old, updates: [{ newValue: value, newVersion: value,
      newMajor: Number(value.split('.')[0]), updateType: type }] };
}
function rule(name, value, generation = 123, isolate = true) {
  // Exercise the production rule builder, not an imitation of its policy.
  const controller = process.env.RENOVATE_POLICY_SCRIPT ?? fileURLToPath(new URL('./renovate_policy.py', import.meta.url));
  const code = "import importlib.util,json,sys; s=importlib.util.spec_from_file_location('policy',sys.argv[1]); m=importlib.util.module_from_spec(s); s.loader.exec_module(m); print(json.dumps(m.quarantine_rules(json.loads(sys.argv[2]),int(sys.argv[3]))))";
  const [result] = JSON.parse(execFileSync('python3', ['-c', code, controller,
    JSON.stringify([{depName:name,newValue:value,datasource:'npm'}]), String(generation)],
    { encoding:'utf8', timeout:5000 }));
  if (!isolate) {
    // Negative control: the earlier scoped-only proposal lacks isolation.
    delete result.groupName;
    delete result.additionalBranchPrefix;
  }
  return result;
}
function config(rules, { grouped = true, always = true } = {}) {
  return { ...getConfig(), semanticCommits: 'disabled', errors: [], warnings: [],
    repoIsOnboarded: true, baseBranch: 'main', dependencyDashboard: true,
    recreateWhen: always ? 'always' : 'auto', packageRules: [
      ...(grouped ? [{ matchPackageNames: ['*'], groupName: 'weekly' }] : []), ...rules] };
}
async function branches(cfg, deps) {
  return (await branchifyUpgrades(cfg, { npm: [{ packageFile: 'package.json', deps }] })).branches;
}
async function gate(branch, checks = {}) {
  try {
    return await processBranch({ ...branch, cacheFingerprintMatch: 'no-match', dependencyDashboardChecks: checks });
  } catch (error) {
    if (error.fixtureBoundary) return { result: 'passed-approval-boundary' };
    throw error;
  }
}
async function explicitApproval(branch, cfg) {
  checkedBody = ` - [x] <!-- approve-branch=${branch.branchName} -->`;
  const dashboardConfig = { ...cfg };
  await readDashboardBody(dashboardConfig);
  assert.equal(dashboardConfig.dependencyDashboardChecks[branch.branchName], 'approve');
  return gate(branch, dashboardConfig.dependencyDashboardChecks);
}
const passed = 'passed-approval-boundary';

// Regression proof: scoped never alone hides a new version in the same major.
for (const always of [false, true]) {
  const cfg = config([rule('a', '4.0.0')], { grouped: false, always });
  const [failed] = await branches(cfg, [dependency('a', '4.0.0')]);
  closedBranch = failed.branchName.replace('fin211-retry-123-', '');
  existingBranches = new Set([closedBranch]);
  assert.equal((await gate(failed)).result, 'needs-approval');
  assert.equal((await explicitApproval(failed, cfg)).result, passed);
  const [future] = await branches(cfg, [dependency('a', '4.0.1')]);
  assert.equal((await gate(future)).result, always ? passed : 'already-existed');
}
console.log('PASS: future major patch visible with global always; exact version needs explicit approval');

// Regression proof: one quarantined member must not block a newer group member.
const unsafe = config([rule('a', '4.0.0', 123, false)]);
const [unsafeGroup] = await branches(unsafe, [dependency('a', '4.0.0'), dependency('b', '5.0.0')]);
closedBranch = unsafeGroup.branchName;
existingBranches = new Set([closedBranch]);
assert.equal((await gate(unsafeGroup)).result, 'already-existed');
const mixedConfig = config([rule('a', '4.0.0')]);
const mixed = await branches(mixedConfig, [dependency('a', '4.0.0'), dependency('b', '5.0.0')]);
assert.equal(mixed.length, 2);
const a = mixed.find((branch) => branch.depName === 'a');
const b = mixed.find((branch) => branch.depName === 'b');
assert.equal((await gate(a)).result, 'needs-approval');
assert.equal((await gate(b)).result, passed);
assert.equal(b.branchName, closedBranch);
console.log('PASS: failed member isolated, newer group member remains eligible');

// The exact failed group is split into visible approvals; no recreation loop.
const exactConfig = config([rule('a', '4.0.0'), rule('b', '5.0.0')]);
const exact = await branches(exactConfig, [dependency('a', '4.0.0'), dependency('b', '5.0.0')]);
assert.equal(exact.length, 2);
for (const branch of exact) {
  assert.equal((await gate(branch)).result, 'needs-approval');
  assert.equal((await explicitApproval(branch, exactConfig)).result, passed);
}
await ensureDependencyDashboard({ ...exactConfig, dependencyDashboardChecks: {} },
  exact.map((branch) => ({ ...branch, result: 'needs-approval' })), {}, {});
assert.match(dashboardBody, /## Pending Approval/);
for (const branch of exact) assert.ok(dashboardBody.includes(`<!-- approve-branch=${branch.branchName} -->`));
console.log('PASS: same failed group requires approval; real Dashboard renderer/parser support retries');

// A second failure after a manual retry needs a NEW approval despite saved branches.
existingBranches = new Set([...existingBranches, ...exact.map((branch) => branch.branchName)]);
const nextConfig = config([rule('a', '4.0.0', 456), rule('b', '5.0.0', 456)]);
const next = await branches(nextConfig, [dependency('a', '4.0.0'), dependency('b', '5.0.0')]);
for (const branch of next) {
  assert.ok(branch.branchName.includes('fin211-retry-456-'));
  assert.equal((await gate(branch)).result, 'needs-approval');
}
console.log('PASS: next generation needs fresh approval and preserves earlier branches');

// Ordinary minor fixes are not matched by the failed exact-value rule either.
const minorConfig = config([rule('a', '3.1.0')], { grouped: false });
const [minor] = await branches(minorConfig, [dependency('a', '3.1.1', '3.0.0', 'minor')]);
assert.equal(minor.dependencyDashboardApproval, false);
assert.equal((await gate(minor)).result, passed);
console.log('PASS: newer minor/patch values remain eligible');

// Moving tags must not hide a newer security/fixed digest under the same tag.
const { applyPackageRules } = await import(mod('util/package-rules/index.js'));
const controllerPath = process.env.RENOVATE_POLICY_SCRIPT ?? fileURLToPath(new URL('./renovate_policy.py', import.meta.url));
const digestInput = [{ depName:'postgres', datasource:'docker', newValue:'18', newDigest:'sha256:bad' }];
const digestCode = "import importlib.util,json,sys; s=importlib.util.spec_from_file_location('policy',sys.argv[1]); m=importlib.util.module_from_spec(s); s.loader.exec_module(m); print(json.dumps(m.quarantine_rules(json.loads(sys.argv[2]),99)))";
const digestRules = JSON.parse(execFileSync('python3',['-c',digestCode,controllerPath,JSON.stringify(digestInput)],{encoding:'utf8',timeout:5000}));
for (const newDigest of ['sha256:bad','sha256:fixed']) {
  const matched = await applyPackageRules({ packageName:'postgres', depName:'postgres', datasource:'docker', newValue:'18', newDigest, packageRules:digestRules }, 'test');
  assert.equal(matched.dependencyDashboardApproval === true, newDigest === 'sha256:bad');
}
console.log('PASS: fixed digest under the same tag escapes exact failed-digest approval');
