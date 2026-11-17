import { Template, defaultBuildLogger } from 'e2b'
import { template } from './template.js'

// Builds the template into the account E2B_API_KEY belongs to. The name is
// what a sandbox is created from, and what goes in a team's settings.
const name = process.env.TEMPLATE_NAME ?? 'openarity-sandbox'

const built = await Template.build(template, name, {
  cpuCount: 1,
  memoryMB: 1024,
  onBuildLogs: defaultBuildLogger(),
})

console.log(`built ${built.name} (id ${built.templateId})`)
