import { spawn } from 'node:child_process'
import { defaultClaudeProjectDirs } from '../local/claude.ts'
import { defaultCodexSessionDirs } from '../local/codex.ts'
import { fileSize, isDirectory, walkJsonlFiles } from '../local/files.ts'
import type { ProviderId } from '../schema/model.ts'

export type BenchmarkCommandSpec = {
	id: string
	providerId: ProviderId
	command: string
	args: string[]
}

export type DatasetSummary = {
	providerId: ProviderId
	paths: string[]
	files: number
	bytes: number
	missingDirectories: string[]
}

export type BenchmarkCommandResult = BenchmarkCommandSpec & {
	executed: boolean
	ok: boolean | null
	exitCode: number | null
	signal: string | null
	durationMs: number | null
	timedOut: boolean
	stdoutBytes: number
	stderrBytes: number
	stdoutSample: string
	stderrSample: string
	error: string | null
}

export type CcusageBenchmarkOptions = {
	provider?: ProviderId | 'all' | undefined
	execute?: boolean | undefined
	timeoutMs?: number | undefined
	sampleBytes?: number | undefined
	env?: Record<string, string | undefined> | undefined
}

const DEFAULT_TIMEOUT_MS = 30_000
const DEFAULT_SAMPLE_BYTES = 16_384

export function ccusageBenchmarkCommands(
	provider: ProviderId | 'all' = 'all',
): BenchmarkCommandSpec[] {
	const commands: BenchmarkCommandSpec[] = [
		{
			id: 'ccusage-daily',
			providerId: 'claude',
			command: 'bunx',
			args: ['-p', 'ccusage@18.0.11', 'ccusage', 'daily', '--json'],
		},
		{
			id: 'ccusage-weekly',
			providerId: 'claude',
			command: 'bunx',
			args: ['-p', 'ccusage@18.0.11', 'ccusage', 'weekly', '--json'],
		},
		{
			id: 'ccusage-monthly',
			providerId: 'claude',
			command: 'bunx',
			args: ['-p', 'ccusage@18.0.11', 'ccusage', 'monthly', '--json'],
		},
		{
			id: 'ccusage-sessions',
			providerId: 'claude',
			command: 'bunx',
			args: ['-p', 'ccusage@18.0.11', 'ccusage', 'session', '--json'],
		},
		{
			id: 'ccusage-blocks',
			providerId: 'claude',
			command: 'bunx',
			args: ['-p', 'ccusage@18.0.11', 'ccusage', 'blocks', '--json'],
		},
		{
			id: 'ccusage-codex-daily',
			providerId: 'codex',
			command: 'bunx',
			args: ['-p', '@ccusage/codex@18.0.11', 'ccusage-codex', 'daily', '--json'],
		},
		{
			id: 'ccusage-codex-monthly',
			providerId: 'codex',
			command: 'bunx',
			args: ['-p', '@ccusage/codex@18.0.11', 'ccusage-codex', 'monthly', '--json'],
		},
		{
			id: 'ccusage-codex-sessions',
			providerId: 'codex',
			command: 'bunx',
			args: ['-p', '@ccusage/codex@18.0.11', 'ccusage-codex', 'session', '--json'],
		},
	]
	return provider === 'all'
		? commands
		: commands.filter((command) => command.providerId === provider)
}

export async function summarizeUsageDatasets(
	options: Pick<CcusageBenchmarkOptions, 'provider' | 'env'> = {},
): Promise<DatasetSummary[]> {
	const provider = options.provider ?? 'all'
	const summaries: DatasetSummary[] = []
	if (provider === 'all' || provider === 'claude') {
		summaries.push(await summarizeDataset('claude', defaultClaudeProjectDirs(options.env)))
	}
	if (provider === 'all' || provider === 'codex') {
		summaries.push(await summarizeDataset('codex', defaultCodexSessionDirs(options.env)))
	}
	return summaries
}

export async function buildCcusageBenchmark(options: CcusageBenchmarkOptions = {}) {
	const provider = options.provider ?? 'all'
	const timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS
	const sampleBytes = options.sampleBytes ?? DEFAULT_SAMPLE_BYTES
	const commands = ccusageBenchmarkCommands(provider)
	const datasets = await summarizeUsageDatasets({ provider, env: options.env })
	const results = options.execute
		? await runBenchmarkCommands(commands, { timeoutMs, sampleBytes, env: options.env })
		: commands.map((command) => skippedResult(command))

	return {
		generatedAt: new Date().toISOString(),
		execute: options.execute === true,
		timeoutMs,
		sampleBytes,
		datasets,
		results,
	}
}

async function summarizeDataset(providerId: ProviderId, paths: string[]): Promise<DatasetSummary> {
	const missingDirectories: string[] = []
	let files = 0
	let bytes = 0

	for (const path of paths) {
		if (!(await isDirectory(path))) {
			missingDirectories.push(path)
			continue
		}
		for await (const filePath of walkJsonlFiles(path)) {
			files += 1
			bytes += await fileSize(filePath)
		}
	}

	return { providerId, paths, files, bytes, missingDirectories }
}

async function runBenchmarkCommands(
	commands: BenchmarkCommandSpec[],
	options: Required<Pick<CcusageBenchmarkOptions, 'timeoutMs' | 'sampleBytes'>> &
		Pick<CcusageBenchmarkOptions, 'env'>,
): Promise<BenchmarkCommandResult[]> {
	const results: BenchmarkCommandResult[] = []
	for (const command of commands) {
		results.push(await runBenchmarkCommand(command, options))
	}
	return results
}

function skippedResult(command: BenchmarkCommandSpec): BenchmarkCommandResult {
	return {
		...command,
		executed: false,
		ok: null,
		exitCode: null,
		signal: null,
		durationMs: null,
		timedOut: false,
		stdoutBytes: 0,
		stderrBytes: 0,
		stdoutSample: '',
		stderrSample: '',
		error: null,
	}
}

async function runBenchmarkCommand(
	spec: BenchmarkCommandSpec,
	options: Required<Pick<CcusageBenchmarkOptions, 'timeoutMs' | 'sampleBytes'>> &
		Pick<CcusageBenchmarkOptions, 'env'>,
): Promise<BenchmarkCommandResult> {
	const started = performance.now()
	const sampleBytes = options.sampleBytes ?? DEFAULT_SAMPLE_BYTES
	const child = spawn(spec.command, spec.args, {
		stdio: ['ignore', 'pipe', 'pipe'],
		env: processEnv(options.env),
	})
	let timedOut = false
	let stdoutBytes = 0
	let stderrBytes = 0
	let stdoutSample = ''
	let stderrSample = ''

	const appendSample = (current: string, chunk: Buffer) =>
		current.length >= sampleBytes
			? current
			: `${current}${chunk.toString('utf8').slice(0, sampleBytes - current.length)}`

	child.stdout.on('data', (chunk: Buffer) => {
		stdoutBytes += chunk.byteLength
		stdoutSample = appendSample(stdoutSample, chunk)
	})
	child.stderr.on('data', (chunk: Buffer) => {
		stderrBytes += chunk.byteLength
		stderrSample = appendSample(stderrSample, chunk)
	})

	const timeout = setTimeout(() => {
		timedOut = true
		child.kill('SIGTERM')
	}, options.timeoutMs)

	const result = await new Promise<{
		exitCode: number | null
		signal: string | null
		error: string | null
	}>((resolve) => {
		child.once('error', (error) => {
			resolve({ exitCode: null, signal: null, error: error.message })
		})
		child.once('close', (exitCode, signal) => {
			resolve({ exitCode, signal, error: null })
		})
	})
	clearTimeout(timeout)

	const durationMs = Math.round(performance.now() - started)
	return {
		...spec,
		executed: true,
		ok: result.error == null && result.exitCode === 0 && !timedOut,
		exitCode: result.exitCode,
		signal: result.signal,
		durationMs,
		timedOut,
		stdoutBytes,
		stderrBytes,
		stdoutSample,
		stderrSample,
		error: result.error,
	}
}

function processEnv(env: Record<string, string | undefined> | undefined): NodeJS.ProcessEnv {
	if (env == null) {
		return process.env
	}
	return Object.fromEntries(
		Object.entries({ ...process.env, ...env }).filter((entry): entry is [string, string] => {
			const value = entry[1]
			return value != null
		}),
	)
}
