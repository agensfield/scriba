import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

export async function withTempDir<T>(prefix: string, fn: (path: string) => Promise<T>): Promise<T> {
	const path = await mkdtemp(join(tmpdir(), prefix))
	try {
		return await fn(path)
	} finally {
		await rm(path, { recursive: true, force: true })
	}
}
