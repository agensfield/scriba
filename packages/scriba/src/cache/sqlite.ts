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
		`)
	}

	static async open(options: ScribaCacheOptions = {}): Promise<ScribaCache> {
		const cacheDir = resolveCacheDir(options.cacheDir, options.env)
		await mkdir(cacheDir, { recursive: true })
		const { Database } = await import('bun:sqlite')
		return new ScribaCache(new Database(join(cacheDir, 'scriba.sqlite'), { create: true }), {
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

	status(): CacheStatus {
		const snapshots = this.db
			.query('select name, updated_at as updatedAt from snapshots order by name')
			.all() as CacheStatus['snapshots']
		const scanStats = this.db
			.query(
				'select provider_id as providerId, json, updated_at as updatedAt from scan_stats order by provider_id',
			)
			.all() as Array<{ providerId: ProviderId; json: string; updatedAt: string }>
		return {
			cacheDir: this.cacheDir,
			databasePath: this.databasePath,
			snapshots,
			scanStats: scanStats.map((row) => ({
				providerId: row.providerId,
				updatedAt: row.updatedAt,
				stats: JSON.parse(row.json) as ScannerStats,
			})),
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

export async function resetCache(options: ScribaCacheOptions = {}): Promise<string> {
	const cacheDir = resolveCacheDir(options.cacheDir, options.env)
	await rm(cacheDir, { recursive: true, force: true })
	return cacheDir
}
