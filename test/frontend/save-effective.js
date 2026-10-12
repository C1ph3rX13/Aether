async page => {
  const origin = new URL(page.url()).origin
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const api = async (path, data, method = 'get', status = 200) => {
    const response = await page.request[method](`${origin}/api/v1${path}`, data ? { data } : undefined)
    assert(response.status() === status, `${method} ${path}: HTTP ${response.status()}`)
    return response.json()
  }
  const project = title => api('/projects', { title, origin: 'origin', goal: 'goal', bootstrap_enabled: false }, 'post', 201)
  let read = await api('/configuration')
  const draft = read.recommended_draft
  draft.workers = [{ name: 'original-worker', agent_type: 'codex', task_types: ['bootstrap', 'reason', 'explore'], max_running: 1, priority: 0, capabilities: [], timeout_overrides: {}, agent_env: { CODEX_BASE_URL: 'http://127.0.0.1:PROVIDER_PORT', CODEX_MODEL: 'original-model' } }]
  read = await api('/configuration', { expected_draft_version: read.draft_version, draft, secrets: { 'original-worker': { OPENAI_API_KEY: { value: 'isolated-original-secret' } } } }, 'put')
  const old = await project('Original configuration')
  assert(old.project.config_revision === read.active_revision.id, 'Creation binds the Save receipt')

  // Delete through the actual UI and inspect the effective catalog immediately.
  await page.goto(`${origin}/workers/codex`)
  await page.getByRole('button', { name: 'View original-worker configuration', exact: true }).click()
  await page.getByRole('button', { name: 'Delete original-worker', exact: true }).click()
  const deletion = page.waitForResponse(response => response.request().method() === 'DELETE')
  await page.getByRole('dialog', { name: 'Delete original-worker?', exact: true }).getByRole('button', { name: 'Delete Worker', exact: true }).click()
  read = await (await deletion).json()
  assert(read.draft.workers.length === 0 && read.validation_problems.length === 3, 'Final deletion saves empty catalog and missing tasks')
  assert((await api('/configuration/revisions/active')).configuration.workers.length === 0, 'Deleted catalog is immediately effective')
  const blocked = await api('/projects', { title: 'Blocked', origin: 'origin', goal: 'goal' }, 'post', 503)
  assert(blocked.code === 'configuration_required' && ['bootstrap', 'reason', 'explore'].every(task => blocked.detail.includes(task)), 'Creation explains missing capabilities')
  await page.goto(`${origin}/projects`)
  await page.getByText('Missing Worker capabilities: bootstrap, reason, explore. Add or edit Workers to cover these tasks.', { exact: true }).waitFor()
  assert(await page.getByRole('button', { name: 'New Project', exact: true }).isDisabled(), 'UI blocks new projects while old ones remain visible')
  assert((await api(`/projects/${old.project.id}`)).project.config_revision === old.project.config_revision, 'Deletion preserves old Project binding')

  // Other Settings can still save without Workers.
  await page.goto(`${origin}/settings/runtime-capacity`)
  await page.getByLabel('Scheduler interval (seconds)', { exact: true }).fill('11')
  const saving = page.waitForResponse(response => response.url().endsWith('/api/v1/configuration') && response.request().method() === 'PUT')
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  assert((await saving).ok(), 'Settings Save works with incomplete coverage')
  assert((await api('/configuration/revisions/active')).configuration.runtime.scheduler_interval_seconds === 11, 'Settings Save becomes effective immediately')
  assert(await page.getByRole('button', { name: 'Apply', exact: true }).count() === 0, 'No extra Apply step')

  // Partial coverage is a valid save but must still block creation.
  read = await api('/configuration')
  draft.runtime = read.draft.runtime
  draft.workers[0].name = 'replacement-worker'
  draft.workers[0].task_types = ['reason']
  draft.workers[0].agent_env.CODEX_MODEL = 'replacement-model'
  read = await api('/configuration', { expected_draft_version: read.draft_version, draft, secrets: { 'replacement-worker': { OPENAI_API_KEY: { value: 'isolated-replacement-secret' } } } }, 'put')
  assert(read.validation_problems.length === 2, 'Partial coverage saves successfully')
  const partial = await api('/projects', { title: 'Still blocked', origin: 'origin', goal: 'goal' }, 'post', 503)
  assert(partial.detail.includes('bootstrap') && partial.detail.includes('explore') && !partial.detail.includes('for reason'), 'Only missing capabilities are reported')

  // Edit and Save the remaining coverage, then create without navigating to Apply.
  await page.goto(`${origin}/workers/codex/editor?worker=replacement-worker`)
  await page.getByRole('checkbox', { name: 'Bootstrap', exact: true }).check()
  await page.getByRole('checkbox', { name: 'Explore', exact: true }).check()
  const restored = page.waitForResponse(response => response.url().endsWith('/api/v1/configuration') && response.request().method() === 'PUT')
  await page.getByRole('button', { name: 'Save Worker', exact: true }).click()
  read = await (await restored).json()
  await page.goto(`${origin}/projects`)
  await page.getByText('Project creation ready', { exact: true }).waitFor()
  assert(await page.getByRole('button', { name: 'New Project', exact: true }).isEnabled(), 'Completing coverage automatically restores UI creation')
  const fresh = await project('Replacement configuration')
  assert(fresh.project.config_revision === read.active_revision.id, 'New Project binds replacement Save immediately')
  const oldConfig = await api(`/configuration/revisions/${encodeURIComponent(old.project.config_revision)}`)
  const newConfig = await api(`/configuration/revisions/${encodeURIComponent(fresh.project.config_revision)}`)
  assert(oldConfig.configuration.workers[0].name === 'original-worker' && newConfig.configuration.workers[0].name === 'replacement-worker', 'Old and new Projects use separate immutable Workers')
  assert(!JSON.stringify([oldConfig, newConfig]).includes('isolated-'), 'Read models never reveal credentials')
  return { immediateWorkerAndSettings: 'passed', incompleteCoverage: 'passed', automaticCreationRecovery: 'passed', historicalProjectBinding: 'passed' }
}
