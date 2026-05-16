import { execFileSync } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const root = join(import.meta.dirname, '..')
const tempPaths = []

try {
	const packDir = makeTempDir('scriba-pack-')

	run('npm', ['pack', root, '--pack-destination', packDir], { cwd: root })
	const tarball = join(packDir, 'agensfield-scriba-0.0.0-alpha.0.tgz')

	run('npm', ['exec', '--yes', '--package', tarball, '--', 'scriba', '--help'], { stdio: 'ignore' })
	run('bunx', ['-p', tarball, 'scriba', '--help'], { stdio: 'ignore' })

	if (commandWorks('corepack', ['pnpm', '--version'])) {
		run('corepack', ['pnpm', 'dlx', tarball, 'scriba', '--help'], { stdio: 'ignore' })
	}

	if (commandWorks('corepack', ['yarn', '--version'])) {
		const yarnConsumer = makeTempDir('scriba-yarn-consumer-')
		run('corepack', ['yarn', 'init', '-y'], { cwd: yarnConsumer, stdio: 'ignore' })
		run('corepack', ['yarn', 'add', tarball], { cwd: yarnConsumer, stdio: 'ignore' })
		run('corepack', ['yarn', 'scriba', '--help'], { cwd: yarnConsumer, stdio: 'ignore' })
		run('corepack', ['yarn', 'scriba', 'cache', 'status'], { cwd: yarnConsumer, stdio: 'ignore' })
	}

	const consumer = makeTempDir('scriba-npm-consumer-')
	run('npm', ['install', '--prefix', consumer, tarball])
	run(join(consumer, 'node_modules', '.bin', 'scriba'), ['cache', 'status'], { stdio: 'ignore' })
	run(
		'node',
		['-e', 'import("@agensfield/scriba").then((m) => { if (!m.ScribaCache) process.exit(1) })'],
		{ cwd: consumer },
	)

	console.log(`package smoke passed: ${tarball}`)
} finally {
	for (const path of tempPaths.reverse()) {
		rmSync(path, { recursive: true, force: true })
	}
}

function makeTempDir(prefix) {
	const path = mkdtempSync(join(tmpdir(), prefix))
	tempPaths.push(path)
	return path
}

function commandWorks(command, args) {
	try {
		run(command, args, { stdio: 'ignore' })
		return true
	} catch {
		return false
	}
}

function run(command, args, options = {}) {
	execFileSync(command, args, {
		cwd: options.cwd ?? tmpdir(),
		stdio: options.stdio ?? 'inherit',
		env: process.env,
	})
}
