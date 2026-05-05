import { existsSync, statSync } from 'node:fs'
import { mkdir, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import Database from 'libsql'
import type { ScannerStats } from '../local/types.ts'
import type { ProviderId, StatusSnapshot } from '../schema/model.ts'
import { resolveCacheDir } from './paths.ts'

export const CACHE_SCHEMA_VERSION = 1

export type CacheStatus = {
	cacheDir: string
	databasePath: string
	schemaVersion: number
	sizeBytes: number
	snapshots: Array<{
		name: string
		updatedAt: string
	}>
	scanStats: Array<{
		providerId: ProviderId
		updatedAt: string
		stats: ScannerStats
	}>
	fileEvents: Array<{
		providerId: ProviderId
		files: number
		updatedAt: string
	}>
	wal: {
		enabled: boolean
		mode: string
		busyTimeoutMs: number
	}
}

export type CachedFileEvents<T> = {
	path: string
	size: number
	mtimeMs: number
	stats: ScannerStats
	events: T[]
}

export type ScribaCacheOptions = {
	cacheDir?: string | undefined
	env?: Record<string, string | undefined> | undefined
}

export class ScribaCache {
	readonly cacheDir: string
	readonly databasePath: string
	readonly db: CacheDatabase

	private constructor(db: CacheDatabase, options: ScribaCacheOptions = {}) {
		this.cacheDir = resolveCacheDir(options.cacheDir, options.env)
		this.databasePath = join(this.cacheDir, 'scriba.sqlite')
		this.db = db
		this.db.exec(`
			pragma journal_mode = wal;
			pragma busy_timeout = 5000;
		`)
		this.db.exec(`
			create table if not exists snapshots (
				name text primary key,
				json text not null,
				updated_at text not null
			);
			create table if not exists meta (
				key text primary key,
				value text not null
			);
			create table if not exists scan_stats (
				provider_id text primary key,
				json text not null,
				updated_at text not null
			);
			create table if not exists file_events (
				provider_id text not null,
				path text not null,
				size integer not null,
				mtime_ms real not null,
				events_json text not null,
				stats_json text not null,
				updated_at text not null,
				primary key (provider_id, path)
			);
		`)
		this.ensureSchemaVersion()
	}

	static async open(options: ScribaCacheOptions = {}): Promise<ScribaCache> {
		const cacheDir = resolveCacheDir(options.cacheDir, options.env)
		await mkdir(cacheDir, { recursive: true })
		return new ScribaCache(openCacheDatabase(join(cacheDir, 'scriba.sqlite')), {
			...options,
			cacheDir,
		})
	}

	saveSnapshot(name: string, snapshot: unknown, updatedAt = new Date().toISOString()): void {
		this.db
			.query('insert or replace into snapshots (name, json, updated_at) values (?, ?, ?)')
			.run(name, JSON.stringify(snapshot), updatedAt)
	}

	loadSnapshot<T = unknown>(name: string): T | null {
		const row = this.db.query('select json from snapshots where name = ?').get(name) as
			| { json: string }
			| undefined
		return row == null ? null : (JSON.parse(row.json) as T)
	}

	saveScanStats(
		providerId: ProviderId,
		stats: ScannerStats,
		updatedAt = new Date().toISOString(),
	): void {
		this.db
			.query('insert or replace into scan_stats (provider_id, json, updated_at) values (?, ?, ?)')
			.run(providerId, JSON.stringify(stats), updatedAt)
	}

	loadFileEvents<T>(
		providerId: ProviderId,
		path: string,
		fingerprint: { size: number; mtimeMs: number },
	): CachedFileEvents<T> | null {
		const row = this.db
			.query(
				'select size, mtime_ms as mtimeMs, events_json as eventsJson, stats_json as statsJson from file_events where provider_id = ? and path = ?',
			)
			.get(providerId, path) as
			| { size: number; mtimeMs: number; eventsJson: string; statsJson: string }
			| undefined
		if (row == null || row.size !== fingerprint.size || row.mtimeMs !== fingerprint.mtimeMs) {
			return null
		}
		return {
			path,
			size: row.size,
			mtimeMs: row.mtimeMs,
			stats: JSON.parse(row.statsJson) as ScannerStats,
			events: JSON.parse(row.eventsJson) as T[],
		}
	}

	saveFileEvents<T>(
		providerId: ProviderId,
		path: string,
		fingerprint: { size: number; mtimeMs: number },
		events: T[],
		stats: ScannerStats,
		updatedAt = new Date().toISOString(),
	): void {
		this.db
			.query(
				'insert or replace into file_events (provider_id, path, size, mtime_ms, events_json, stats_json, updated_at) values (?, ?, ?, ?, ?, ?, ?)',
			)
			.run(
				providerId,
				path,
				fingerprint.size,
				fingerprint.mtimeMs,
				JSON.stringify(events),
				JSON.stringify(stats),
				updatedAt,
			)
	}

	status(): CacheStatus {
		const schemaVersion =
			Number(
				(
					this.db.query('select value from meta where key = ?').get('schema_version') as
						| { value: string }
						| undefined
				)?.value,
			) || 0
		const walMode = (
			this.db.query('pragma journal_mode').get() as { journal_mode?: string } | undefined
		)?.journal_mode
		const snapshots = this.db
			.query('select name, updated_at as updatedAt from snapshots order by name')
			.all() as CacheStatus['snapshots']
		const scanStats = this.db
			.query(
				'select provider_id as providerId, json, updated_at as updatedAt from scan_stats order by provider_id',
			)
			.all() as Array<{ providerId: ProviderId; json: string; updatedAt: string }>
		const fileEvents = this.db
			.query(
				'select provider_id as providerId, count(*) as files, max(updated_at) as updatedAt from file_events group by provider_id order by provider_id',
			)
			.all() as CacheStatus['fileEvents']
		return {
			cacheDir: this.cacheDir,
			databasePath: this.databasePath,
			schemaVersion,
			sizeBytes: databaseSizeBytes(this.databasePath),
			snapshots,
			scanStats: scanStats.map((row) => ({
				providerId: row.providerId,
				updatedAt: row.updatedAt,
				stats: JSON.parse(row.json) as ScannerStats,
			})),
			fileEvents,
			wal: {
				enabled: walMode === 'wal',
				mode: walMode ?? 'unknown',
				busyTimeoutMs: 5000,
			},
		}
	}

	pruneFileEvents(existingPaths: Set<string>, updatedAt = new Date().toISOString()): number {
		let pruned = 0
		const rows = this.db
			.query('select provider_id as providerId, path from file_events')
			.all() as Array<{
			providerId: ProviderId
			path: string
		}>
		for (const row of rows) {
			if (existingPaths.has(row.path)) {
				continue
			}
			this.db
				.query('delete from file_events where provider_id = ? and path = ?')
				.run(row.providerId, row.path)
			pruned += 1
		}
		if (pruned > 0) {
			this.db
				.query('insert or replace into meta (key, value) values (?, ?)')
				.run('last_pruned_at', updatedAt)
		}
		return pruned
	}

	vacuum(): void {
		this.db.exec('vacuum')
	}

	async writeJsonSnapshot(name: string, snapshot: StatusSnapshot): Promise<string> {
		const path = join(this.cacheDir, `${name}.json`)
		await writeFile(path, `${JSON.stringify(snapshot, null, 2)}\n`)
		return path
	}

	close(): void {
		this.db.close()
	}

	private ensureSchemaVersion(): void {
		const row = this.db.query('select value from meta where key = ?').get('schema_version') as
			| { value: string }
			| undefined
		const version = Number(row?.value ?? 0)
		if (version > 0 && version !== CACHE_SCHEMA_VERSION) {
			this.db.exec(`
				delete from snapshots;
				delete from scan_stats;
				delete from file_events;
			`)
		}
		this.db
			.query('insert or replace into meta (key, value) values (?, ?)')
			.run('schema_version', String(CACHE_SCHEMA_VERSION))
	}
}

type CacheDatabase = {
	exec(sql: string): unknown
	query(sql: string): {
		run(...args: unknown[]): unknown
		get(...args: unknown[]): unknown
		all(...args: unknown[]): unknown[]
	}
	close(): void
}

function openCacheDatabase(databasePath: string): CacheDatabase {
	return new LibsqlCacheDatabase(new Database(databasePath))
}

type LibsqlDatabase = {
	exec(sql: string): unknown
	prepare(sql: string): {
		run(...args: unknown[]): unknown
		get(...args: unknown[]): unknown
		all(...args: unknown[]): unknown[]
	}
	close(): void
}

class LibsqlCacheDatabase implements CacheDatabase {
	constructor(private readonly database: LibsqlDatabase) {}

	exec(sql: string): unknown {
		return this.database.exec(sql)
	}

	query(sql: string): ReturnType<CacheDatabase['query']> {
		const statement = this.database.prepare(sql)
		return {
			run: (...args: unknown[]) => statement.run(...args),
			get: (...args: unknown[]) => statement.get(...args),
			all: (...args: unknown[]) => statement.all(...args),
		}
	}

	close(): void {
		this.database.close()
	}
}

function databaseSizeBytes(databasePath: string): number {
	const paths = [databasePath, `${databasePath}-wal`, `${databasePath}-shm`]
	return paths.reduce((sum, path) => sum + (existsSync(path) ? statSync(path).size : 0), 0)
}

export async function resetCache(options: ScribaCacheOptions = {}): Promise<string> {
	const cacheDir = resolveCacheDir(options.cacheDir, options.env)
	await rm(cacheDir, { recursive: true, force: true })
	return cacheDir
}
