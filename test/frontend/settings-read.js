async page => {
  const selectOption = async (trigger, label) => {
    await trigger.click()
    await page.getByRole('option', { name: label, exact: true }).click()
  }
  const origin = new URL(page.url()).origin
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const canonical = value => Array.isArray(value) ? value.map(canonical)
    : value && typeof value === 'object' ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([key, item]) => [key, canonical(item)])) : value
  const same = (actual, expected, message) => assert(JSON.stringify(canonical(actual)) === JSON.stringify(canonical(expected)), message)
  const api = async (path, data, method = 'get') => {
    const response = await page.request[method](`${origin}/api/v1${path}`, data ? { data } : undefined)
    assert(response.ok(), `${method} ${path}: HTTP ${response.status()}`)
    return response.json()
  }
  const clean = draft => {
    const result = structuredClone(draft)
    for (const worker of result.workers) delete worker.secrets
    return result
  }
  const waitSave = () => page.waitForResponse(response => response.url().endsWith('/api/v1/configuration') && response.request().method() === 'PUT')
  const navDialog = () => page.getByRole('dialog', { name: 'Unsaved settings', exact: true })
  const settingsPage = () => page.getByRole('region', { name: /^(Tasks|Runtime)$/ })
  const input = name => page.getByLabel(name, { exact: true })
  const uniqueSectionRegion = async () => {
    const title = await page.locator('#settings-section-title').innerText()
    assert(await page.getByRole('region', { name: title, exact: true }).count() === 1, `${title} has one named content region`)
  }
  const save = async () => {
    const pending = waitSave()
    await settingsPage().getByRole('button', { name: 'Save', exact: true }).click()
    const response = await pending
    assert(response.ok(), 'Settings Save succeeds through the real API')
    return { request: response.request().postDataJSON(), read: await response.json() }
  }

  await page.setViewportSize({ width: 1440, height: 900 })
  const initial = await api('/configuration')
  const draft = initial.recommended_draft
  draft.runtime.scheduler_interval_seconds = 7
  draft.tasks.reason.primary_seconds = 450
  draft.workers = [{
    name: 'settings-worker', agent_type: 'codex', provider_name: 'Settings Provider',
    task_types: ['bootstrap', 'reason', 'explore'], max_running: 2, priority: 3,
    capabilities: [], timeout_overrides: { 'reason.primary': 400 },
    agent_env: { CODEX_BASE_URL: 'http://127.0.0.1:PROVIDER_PORT', CODEX_MODEL: 'fixture' },
  }]
  const seeded = await api('/configuration', {
    expected_draft_version: initial.draft_version, draft,
    secrets: { 'settings-worker': { OPENAI_API_KEY: { value: 'isolated-settings-secret' } } },
  }, 'put')
  const prior = await api('/configuration/revisions/active')
  const project = await api('/projects', { title: 'Settings binding acceptance', origin: 'Existing settings', goal: 'Keep original settings', bootstrap_enabled: false }, 'post')
  assert(project.project.config_revision === prior.id, 'Existing Project binds prior applied configuration')

  // A Project Save owns tasks/retry/container and preserves captured System and Workers.
  await page.goto(`${origin}/settings/task-timeouts`)
  await input('Reason primary (seconds)').fill('777')
  await uniqueSectionRegion()
  assert(await page.getByRole('link', { name: 'Tasks', exact: true }).getAttribute('aria-current') === 'page', 'Project Settings is a supported shell destination')
  const projectSave = await save()
  assert(projectSave.request.expected_draft_version === seeded.draft_version, 'Project Save uses its captured version')
  assert(projectSave.request.draft.tasks.reason.primary_seconds === 777, 'Edited timeout is saved')
  same(projectSave.request.draft.runtime, seeded.draft.runtime, 'Project Save preserves System')
  same(projectSave.read.draft.workers, seeded.draft.workers, 'Project Save preserves Workers and configured credentials')
  assert(projectSave.request.draft.workers.every(worker => !('secrets' in worker)), 'Settings Save excludes read-only Secret projections')
  assert(!('secrets' in projectSave.request), 'Settings Save does not replace credentials')
  assert((await api('/configuration/revisions/active')).configuration.tasks.reason.primary_seconds === 777, 'Save immediately makes edited fields effective')
  await page.screenshot({ path: 'dist/frontend-catalog/settings-project-1440.png', animations: 'disabled' })

  // Cancel preserves the intended destination and input; Save-and-leave uses the next baseline.
  await input('Reason primary (seconds)').fill('888')
  await page.getByRole('link', { name: 'Runtime', exact: true }).click()
  await navDialog().getByRole('button', { name: 'Cancel', exact: true }).click()
  assert(new URL(page.url()).pathname === '/settings/task-timeouts', 'Cancel stays on Project Settings')
  assert(await input('Reason primary (seconds)').inputValue() === '888', 'Cancel keeps local edits')
  await page.getByRole('link', { name: 'Runtime', exact: true }).click()
  const leavingSave = waitSave()
  await navDialog().getByRole('button', { name: 'Save', exact: true }).click()
  const leavingResponse = await leavingSave
  assert(leavingResponse.ok(), 'Save-and-leave commits before navigation')
  assert(leavingResponse.request().postDataJSON().expected_draft_version === projectSave.read.draft_version, 'Next Save uses returned authoritative baseline')
  const savedProject = await leavingResponse.json()
  await navDialog().waitFor({ state: 'hidden' })
  await page.getByRole('tab', { name: 'Project', exact: true }).waitFor()
  assert(new URL(page.url()).pathname === '/settings/project-runtime', 'Save-and-leave reaches the requested Runtime category')
  await page.getByRole('tab', { name: 'Capacity', exact: true }).click()
  await input('Scheduler interval (seconds)').waitFor()

  // System editing preserves Project and Worker groups and becomes effective on Save.
  await input('Scheduler interval (seconds)').fill('11')
  await uniqueSectionRegion()
  assert(await page.getByRole('button', { name: 'Apply', exact: true }).count() === 0, 'There is no global Apply action')
  const systemSave = await save()
  assert(systemSave.request.expected_draft_version === savedProject.draft_version, 'System captures its own complete Draft/version')
  for (const group of ['tasks', 'retry', 'container', 'workers']) same(systemSave.read.draft[group], savedProject.draft[group], `System Save preserves ${group}`)
  const active = await api('/configuration/revisions/active')
  assert(active.id === systemSave.read.active_revision.id, 'Save returns the effective revision')
  assert(active.configuration.tasks.reason.primary_seconds === 888 && active.configuration.runtime.scheduler_interval_seconds === 11, 'Save combines saved page scopes')
  assert(!JSON.stringify(active).includes('isolated-settings-secret'), 'Applied API never exposes stored credentials')
  assert((await api(`/projects/${project.project.id}`)).project.config_revision === prior.id, 'Save preserves existing Project binding')
  const next = await api('/projects', { title: 'Applied Settings acceptance', origin: 'Saved settings', goal: 'Use latest settings', bootstrap_enabled: false }, 'post')
  assert(next.project.config_revision === active.id, 'New Project uses applied settings')
  await page.getByText('Project creation ready', { exact: true }).waitFor()
  await page.screenshot({ path: 'dist/frontend-catalog/settings-system-1440.png', animations: 'disabled' })

  // Inspect every applied scope; the open nonmodal Sheet refreshes after another Save.
  await page.getByRole('button', { name: 'Saved settings', exact: true }).click()
  const panel = page.getByRole('dialog', { name: 'Saved settings', exact: true })
  await panel.getByText('Configured · ********', { exact: true }).waitFor()
  for (const name of ['Project settings', 'System settings', 'Workers']) assert(await panel.getByRole('heading', { name, exact: true }).isVisible(), `Applied inspection includes ${name}`)
  assert(!(await panel.innerText()).includes('isolated-settings-secret'), 'UI masks credentials')
  await page.screenshot({ path: 'dist/frontend-catalog/settings-applied-1440.png', animations: 'disabled' })
  await panel.getByRole('button', { name: 'Close', exact: true }).click()
  await input('Scheduler interval (seconds)').fill('13')
  const nextSave = await save()
  await page.getByRole('button', { name: 'Saved settings', exact: true }).click()
  await panel.getByText('Configured · ********', { exact: true }).waitFor()
  await page.waitForFunction(() => {
    const panel = document.querySelector('[role="dialog"]')
    const label = [...(panel?.querySelectorAll('dt') ?? [])].find(element => element.textContent === 'Scheduler interval (seconds)')
    return label?.nextElementSibling?.textContent === '13'
  })
  assert((await api('/configuration/revisions/active')).configuration.runtime.scheduler_interval_seconds === 13, 'Open inspection refreshes from the applied API')
  await panel.getByRole('button', { name: 'Close', exact: true }).click()

  // Real CAS conflict retains input; only an explicitly successful Reload replaces it.
  const concurrent = clean(nextSave.read.draft)
  concurrent.runtime.scheduler_interval_seconds = 17
  const otherSave = await api('/configuration', { expected_draft_version: nextSave.read.draft_version, draft: concurrent }, 'put')
  await input('Scheduler interval (seconds)').fill('19')
  const conflictingSave = waitSave()
  await page.getByRole('button', { name: 'Save', exact: true }).click()
  assert((await conflictingSave).status() === 409, 'Real CAS conflict rejects the stale page')
  const conflict = page.getByRole('dialog', { name: 'Settings changed elsewhere', exact: true })
  await conflict.getByRole('button', { name: 'Keep editing', exact: true }).click()
  await conflict.waitFor({ state: 'hidden' })
  assert(await input('Scheduler interval (seconds)').inputValue() === '19', 'Conflict preserves local input')
  assert(await page.getByRole('button', { name: 'Save', exact: true }).isDisabled(), 'No automatic conflict retry')
  await settingsPage().getByRole('button', { name: 'Reload', exact: true }).click()
  await conflict.getByRole('button', { name: 'Reload', exact: true }).click()
  await page.waitForFunction(() => document.getElementById('setting-runtime-scheduler_interval_seconds')?.value === '17')
  assert((await api('/configuration')).draft_version === otherSave.draft_version, 'Reload does not write or silently merge')

  // Browser history stays intact through Cancel and exact-destination Discard.
  await page.getByRole('link', { name: 'Projects', exact: true }).click()
  await page.getByRole('table', { name: 'Projects', exact: true }).waitFor()
  await page.getByRole('link', { name: 'Tasks', exact: true }).click()
  await input('Reason primary (seconds)').fill('999')
  await page.evaluate(() => history.back())
  await navDialog().getByRole('button', { name: 'Cancel', exact: true }).click()
  assert(new URL(page.url()).pathname === '/settings/task-timeouts', 'Cancelled Back retains the current history entry')
  assert(await input('Reason primary (seconds)').inputValue() === '999', 'Cancelled Back keeps edits')
  await page.evaluate(() => history.back())
  await navDialog().getByRole('button', { name: 'Discard', exact: true }).click()
  await page.getByRole('table', { name: 'Projects', exact: true }).waitFor()
  await page.goForward()
  await input('Reason primary (seconds)').waitFor()
  assert(await input('Reason primary (seconds)').inputValue() === '888', 'Forward returns to saved values after Discard')
  await page.setViewportSize({ width: 1024, height: 720 })
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Project Settings fits minimum desktop')
  await page.screenshot({ path: 'dist/frontend-catalog/settings-project-1024.png', animations: 'disabled' })
  await page.getByRole('link', { name: 'Runtime', exact: true }).click()
  await page.getByRole('tab', { name: 'Capacity', exact: true }).click()
  await input('Scheduler interval (seconds)').waitFor()
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'System Settings fits minimum desktop')
  await page.screenshot({ path: 'dist/frontend-catalog/settings-system-1024.png', animations: 'disabled' })
  await page.reload()
  await input('Scheduler interval (seconds)').waitFor()
  for (const viewport of [{ width: 320, height: 568 }, { width: 768, height: 1024 }, { width: 844, height: 390 }]) {
    await page.setViewportSize(viewport)
    await input('Scheduler interval (seconds)').waitFor()
    await uniqueSectionRegion()
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Settings fit ${viewport.width} × ${viewport.height}`)
    assert(await page.getByRole('button', { name: 'Save', exact: true }).isVisible(), 'Settings controls remain available')
    if (viewport.width === 320) {
      await page.evaluate(() => { document.documentElement.style.fontSize = '200%' })
      const recommended = page.getByRole('button', { name: 'Use recommended values', exact: true })
      assert(await recommended.evaluate(button => {
        const range = document.createRange()
        range.selectNodeContents(button)
        const bounds = button.getBoundingClientRect()
        return button.scrollWidth <= button.clientWidth && bounds.right <= innerWidth && [...range.getClientRects()].every(rect => rect.left >= bounds.left && rect.right <= bounds.right && rect.top >= bounds.top && rect.bottom <= bounds.bottom)
      }), 'Recommended values remain fully readable at 200% text on mobile')
      assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Settings fit 200% text on mobile')
      await page.evaluate(() => { document.documentElement.style.removeProperty('font-size') })
    }
  }
  for (const obsolete of ['/settings/project', '/settings/system']) assert((await page.request.get(`${origin}${obsolete}`)).status() === 404, 'Obsolete aggregate Settings routes are removed')
  // Each remaining first-level page writes only its owned fields.
  await page.setViewportSize({ width: 1440, height: 900 })
  for (const [section, title, field, value, group, owned] of [
    ['retry-policies', 'Retry Policies', 'Bootstrap maximum attempts', '8', 'retry', null],
    ['project-runtime', 'Project Runtime', 'Network mode', 'bridge', 'container', null],
    ['worker-healthchecks', 'Worker Healthchecks', 'Healthcheck timeout (seconds)', '29', 'runtime', ['healthcheck_timeout_seconds', 'worker_healthcheck']],
    ['prompt-group', 'Prompt Group', 'Prompt Group', 'mock', 'runtime', ['prompt_group']],
  ]) {
    const before = await api('/configuration')
    await page.goto(`${origin}/settings/${section}`)
    await page.locator('[aria-labelledby="settings-section-title"]').filter({ has: page.getByRole('heading', { name: title, exact: true }) }).waitFor()
    await uniqueSectionRegion()
    if (section === 'prompt-group') await selectOption(page.getByRole('combobox', { name: field, exact: true }), value[0].toUpperCase() + value.slice(1))
    else await input(field).fill(value)
    const pending = waitSave()
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    const response = await pending
    assert(response.ok(), `${title} saves through the native API`)
    const after = await response.json()
    for (const sibling of ['tasks', 'retry', 'container', 'runtime', 'workers']) if (sibling !== group) same(after.draft[sibling], before.draft[sibling], `${title} preserves ${sibling}`)
    if (owned) for (const key of Object.keys(before.draft.runtime)) if (!owned.includes(key)) same(after.draft.runtime[key], before.draft.runtime[key], `${title} preserves runtime.${key}`)
    assert(response.request().postDataJSON().expected_draft_version === before.draft_version, 'Section uses captured CAS version')
    assert(await page.getByRole('button', { name: 'Saved settings', exact: true }).isVisible() && await page.getByRole('button', { name: 'Apply', exact: true }).count() === 0, 'Every section exposes saved inspection without Apply')
  }
  return { pageScopedSaves: 'passed', saveEffective: 'passed', uniqueSectionRegions: 'passed', textResize: 'passed', maskedInspectionAndRefresh: 'passed', realCASConflict: 'passed', guardedNavigationAndHistory: 'passed', projectBindingsAndAvailability: 'passed', responsiveAndDeepLinks: 'passed' }
}
