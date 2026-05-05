import { mkdir, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import type { ScannerStats } from '../local/types.ts'
import type { ProviderId, StatusSnapshot } from '../schema/model.ts'
import { resolveCacheDir } from './paths.ts'

export type CacheStatus = {
	cacheDir: string
	databasePath: string
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
	}

	static async open(options: ScribaCacheOptions = {}): Promise<ScribaCache> {
		const cacheDir = resolveCacheDir(options.cacheDir, options.env)
		await mkdir(cacheDir, { recursive: true })
		return new ScribaCache(await openCacheDatabase(join(cacheDir, 'scriba.sqlite')), {
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
			snapshots,
			scanStats: scanStats.map((row) => ({
				providerId: row.providerId,
				updatedAt: row.updatedAt,
				stats: JSON.parse(row.json) as ScannerStats,
			})),
			fileEvents,
		}
	}

	async writeJsonSnapshot(name: string, snapshot: StatusSnapshot): Promise<string> {
		const path = join(this.cacheDir, `${name}.json`)
		await writeFile(path, `${JSON.stringify(snapshot, null, 2)}\n`)
		return path
	}

	close(): void {
		this.db.close()
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

async function openCacheDatabase(databasePath: string): Promise<CacheDatabase> {
	if (isBunRuntime()) {
		return openBunDatabase(databasePath)
	}
	return openNodeDatabase(databasePath)
}

function isBunRuntime(): boolean {
	return typeof (globalThis as { Bun?: unknown }).Bun !== 'undefined'
}

async function openBunDatabase(databasePath: string): Promise<CacheDatabase> {
	const module = (await import('bun:sqlite')) as {
		Database: new (path: string, options: { create: boolean }) => CacheDatabase
	}
	return new module.Database(databasePath, { create: true })
}

async function openNodeDatabase(databasePath: string): Promise<CacheDatabase> {
	const module = (await importNodeSqlite()) as {
		DatabaseSync?: new (path: string) => NodeSqliteDatabase
	}
	if (module.DatabaseSync == null) {
		throw new Error('node:sqlite DatabaseSync is unavailable. Use Node >=24 or run with Bun.')
	}
	return new NodeCacheDatabase(new module.DatabaseSync(databasePath))
}

async function importNodeSqlite(): Promise<unknown> {
	const emitWarning = process.emitWarning
	process.emitWarning = ((warning: string | Error, ...args: unknown[]) => {
		if (String(warning).includes('SQLite is an experimental feature')) {
			return
		}
		return Reflect.apply(emitWarning, process, [warning, ...args])
	}) as typeof process.emitWarning
	try {
		return await import('node:sqlite')
	} catch (error) {
		throw new Error('SQLite cache requires Bun or Node >=24 with node:sqlite.', { cause: error })
	} finally {
		process.emitWarning = emitWarning
	}
}

type NodeSqliteDatabase = {
	exec(sql: string): unknown
	prepare(sql: string): {
		run(...args: unknown[]): unknown
		get(...args: unknown[]): unknown
		all(...args: unknown[]): unknown[]
	}
	close(): void
}

class NodeCacheDatabase implements CacheDatabase {
	constructor(private readonly database: NodeSqliteDatabase) {}

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

export async function resetCache(options: ScribaCacheOptions = {}): Promise<string> {
	const cacheDir = resolveCacheDir(options.cacheDir, options.env)
	await rm(cacheDir, { recursive: true, force: true })
	return cacheDir
}
