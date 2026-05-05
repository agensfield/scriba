import { existsSync } from 'node:fs'
import type { ScribaCache } from '../cache/sqlite.ts'
import type { ScribaConfig } from '../config/schema.ts'
import { isDirectory } from '../local/files.ts'
import { PROVIDERS } from '../providers/descriptors.ts'
import { claudeKeychainServiceExists, claudeKeychainServices } from '../remote/claude.ts'

export type DoctorState = 'ok' | 'degraded' | 'broken'

export type DoctorPayload = {
	generatedAt: string
	state: DoctorState
	cache: {
		state: DoctorState
		cacheDir: string
		databasePath: string
		sizeBytes: number
		schemaVersion: number
		walEnabled: boolean
		latestSnapshotAgeMs: number | null
		error?: string
	}
	providers: Array<{
		providerId: 'claude' | 'codex'
		displayName: string
		state: DoctorState
		localPaths: Array<{ path: string; exists: boolean }>
		auth: {
			state: DoctorState
			paths: Array<{ path: string; exists: boolean }>
			hint: string
		}
		remote: {
			state: DoctorState | 'skipped'
			error?: string
		}
	}>
}

export async function buildDoctorReport(options: {
	config: ScribaConfig
	cache: ScribaCache
	includeRemote?: boolean
	now?: Date
}): Promise<DoctorPayload> {
	const now = options.now ?? new Date()
	const generatedAt = now.toISOString()
	const cacheStatus = options.cache.status()
	const latestSnapshot = cacheStatus.snapshots
		.map((snapshot) => new Date(snapshot.updatedAt).getTime())
		.filter((time) => Number.isFinite(time))
		.sort((a, b) => b - a)[0]
	const providers = []

	for (const descriptor of PROVIDERS) {
		const providerConfig = options.config.providers[descriptor.id]
		const localPathValues =
			providerConfig.paths.length > 0 ? providerConfig.paths : descriptor.defaultLocalPaths()
		const localPaths = await Promise.all(
			localPathValues.map(async (path) => ({ path, exists: await isDirectory(path) })),
		)
		const authPaths = [
			...descriptor.authPaths().map((path) => ({ path, exists: existsSync(path) })),
			...(descriptor.id === 'claude' ? await claudeKeychainAuthPaths() : []),
		]
		const authState: DoctorState = authPaths.some((path) => path.exists) ? 'ok' : 'degraded'
		const localState: DoctorState = localPaths.some((path) => path.exists) ? 'ok' : 'degraded'
		let remote: DoctorPayload['providers'][number]['remote'] = { state: 'skipped' }
		if (options.includeRemote !== false) {
			const result = await descriptor.probeUsage()
			const error = result.provenance.find((source) => source.error != null)?.error
			remote = error == null ? { state: 'ok' } : { state: 'degraded', error }
		}
		const state = worstState([
			localState,
			authState,
			remote.state === 'skipped' ? 'ok' : remote.state,
		])
		providers.push({
			providerId: descriptor.id,
			displayName: descriptor.displayName,
			state,
			localPaths,
			auth: { state: authState, paths: authPaths, hint: descriptor.authHint },
			remote,
		})
	}

	const cacheState: DoctorState =
		cacheStatus.schemaVersion > 0 && cacheStatus.wal.enabled ? 'ok' : 'degraded'
	return {
		generatedAt,
		state: worstState([cacheState, ...providers.map((provider) => provider.state)]),
		cache: {
			state: cacheState,
			cacheDir: cacheStatus.cacheDir,
			databasePath: cacheStatus.databasePath,
			sizeBytes: cacheStatus.sizeBytes,
			schemaVersion: cacheStatus.schemaVersion,
			walEnabled: cacheStatus.wal.enabled,
			latestSnapshotAgeMs:
				latestSnapshot == null ? null : Math.max(0, now.getTime() - latestSnapshot),
		},
		providers,
	}
}

async function claudeKeychainAuthPaths(): Promise<Array<{ path: string; exists: boolean }>> {
	return Promise.all(
		claudeKeychainServices().map(async (service) => ({
			path: `macOS Keychain: ${service}`,
			exists: await claudeKeychainServiceExists(service),
		})),
	)
}

function worstState(states: Array<DoctorState>): DoctorState {
	if (states.includes('broken')) {
		return 'broken'
	}
	if (states.includes('degraded')) {
		return 'degraded'
	}
	return 'ok'
}
