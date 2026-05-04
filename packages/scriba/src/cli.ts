#!/usr/bin/env bun

import { runMain } from 'citty'
import { createRootCommand } from './cli/command.ts'

await runMain(createRootCommand())
