async page => {
  const origin = new URL(page.url()).origin
  await page.setViewportSize({ width: 1440, height: 900 })
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const api = async (path, data, method = 'get') => {
    const response = await page.request[method](`${origin}/api/v1${path}`, data ? { data } : undefined)
    assert(response.ok(), `${method} ${path}: HTTP ${response.status()}`)
    return response.json()
  }
  const branches = [
    { type: 'codex', label: 'Codex', path: 'codex', env: { CODEX_BASE_URL: 'http://127.0.0.1:PROVIDER_PORT', CODEX_MODEL: 'fixture' }, secret: 'OPENAI_API_KEY' },
    { type: 'claude_code', label: 'Claude', path: 'claude', env: { IS_SANDBOX: '1', ANTHROPIC_BASE_URL: 'http://127.0.0.1:PROVIDER_PORT', ANTHROPIC_MODEL: 'fixture' }, secret: 'ANTHROPIC_AUTH_TOKEN' },
    { type: 'pi', label: 'Pi', path: 'pi', env: { PI_BASE_URL: 'http://127.0.0.1:PROVIDER_PORT', PI_MODEL: 'fixture', PI_PROVIDER_API: 'openai-completions' }, secret: 'PI_API_KEY' },
  ]
  const refresh = async () => {
    const response = page.waitForResponse(response => response.url().endsWith('/api/v1/configuration') && response.ok())
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await response
  }
  const card = name => page.getByRole('listitem').filter({ has: page.getByRole('heading', { name, exact: true }) })
  const worker = branch => ({ name: `${branch.path}-saved`, agent_type: branch.type, provider_name: `${branch.label} Provider`, task_types: ['bootstrap', 'reason', 'explore'], max_running: 2, priority: 3, capabilities: [], timeout_overrides: {}, agent_env: branch.env })
  const initial = await api('/configuration')
  const draft = initial.recommended_draft
  draft.workers = branches.map(worker)
  const secrets = Object.fromEntries(branches.map(branch => [`${branch.path}-saved`, { [branch.secret]: { value: `isolated-${branch.path}-secret` } }]))
  const saved = await api('/configuration', { expected_draft_version: initial.draft_version, draft, secrets }, 'put')
  const revision = await api('/configuration/revisions/active')
  const project = await api('/projects', { title: 'Worker deletion binding acceptance', origin: 'Existing Project configuration', goal: 'Retain bound Workers', bootstrap_enabled: false }, 'post')
  assert(project.project.config_revision === revision.id, 'Project binds applied Worker revision')

  const seeded = saved
  assert(!JSON.stringify(seeded).includes('isolated-codex-secret'), 'Configuration read masks Secrets')
  for (const branch of branches) {
    await page.goto(`${origin}/workers/${branch.path}`)
    await page.getByRole('list', { name: `${branch.label} Workers`, exact: true }).waitFor()
    assert(await page.getByRole('link', { name: branch.label, exact: true }).getAttribute('aria-current') === 'page', 'Correct active shell destination')
    assert(await page.getByRole('link', { name: 'Projects', exact: true }).getAttribute('aria-current') === null, 'Projects not falsely active')
    assert(await card(`${branch.path}-saved`).getByText('Configured', { exact: true }).isVisible(), 'Worker Ready describes saved configuration')
    assert(await card(`${branch.path}-saved`).getByText(new RegExp(`${branch.label} Provider`)).isVisible(), 'Provider display preserved')
    for (const task of ['Bootstrap', 'Reason', 'Explore']) assert(await card(`${branch.path}-saved`).getByText(task, { exact: true }).isVisible(), 'Supported tasks preserved')
    const text = await page.locator('body').innerText()
    assert(!text.includes('isolated-') && !text.includes(initial.deployment_id), 'No Secret or deployment identity in shell/catalog')
    await page.screenshot({ path: `dist/frontend-catalog/workers-${branch.path}-1440.png`, animations: 'disabled' })
  }

  // A concurrent real API save must not be overwritten or silently retried.
  await page.getByRole('link', { name: 'Codex', exact: true }).click()
  await page.getByRole('heading', { name: 'codex-saved', exact: true }).waitFor()
  const trigger = page.getByRole('button', { name: 'View codex-saved configuration', exact: true })
  await trigger.focus()
  await page.keyboard.press('Enter')
  await page.getByRole('button', { name: 'Delete codex-saved', exact: true }).click()
  const dialog = page.getByRole('dialog', { name: 'Delete codex-saved?', exact: true })
  await dialog.waitFor()
  assert(await dialog.getByRole('button', { name: 'Cancel', exact: true }).evaluate(element => element === document.activeElement), 'Confirmation starts on safe Cancel')
  await page.keyboard.press('Escape')
  await dialog.waitFor({ state: 'hidden' })
  await page.waitForFunction(() => document.activeElement?.getAttribute('aria-label') === 'View codex-saved configuration')
  assert(await trigger.evaluate(element => element === document.activeElement), 'Cancel restores trigger focus')
  await trigger.click()
  await page.getByRole('button', { name: 'Delete codex-saved', exact: true }).click()
  const concurrent = structuredClone(draft)
  concurrent.workers[0].priority = 7
  const changed = await api('/configuration', { expected_draft_version: seeded.draft_version, draft: concurrent }, 'put')
  const conflict = page.waitForResponse(response => response.request().method() === 'DELETE')
  await dialog.getByRole('button', { name: 'Delete Worker', exact: true }).click()
  assert((await conflict).status() === 409, 'Real Draft CAS conflict')
  await dialog.getByText('Worker settings changed elsewhere. Reload the list and confirm deletion again.', { exact: true }).waitFor()
  assert(await dialog.getByRole('button', { name: 'Delete Worker', exact: true }).isDisabled(), 'No retry on stale version')
  assert((await api('/configuration')).draft.workers.some(worker => worker.name === 'codex-saved'), 'Conflict retains saved Worker')
  await dialog.getByRole('button', { name: 'Reload list', exact: true }).click()
  await dialog.waitFor({ state: 'hidden' })
  await trigger.click()
  const configuration = page.getByRole('dialog', { name: 'Worker configuration', exact: true })
  await configuration.getByText('7', { exact: true }).waitFor()
  await page.keyboard.press('Escape')
  await trigger.click()
  await page.getByRole('button', { name: 'Delete codex-saved', exact: true }).click()
  await page.setViewportSize({ width: 1024, height: 720 })
  await page.screenshot({ path: 'dist/frontend-catalog/workers-delete-1024.png', animations: 'disabled' })
  const deletion = page.waitForResponse(response => response.request().method() === 'DELETE')
  await dialog.getByRole('button', { name: 'Delete Worker', exact: true }).focus()
  await page.keyboard.press('Enter')
  const response = await deletion
  assert(response.ok(), 'Confirmed real deletion succeeds')
  assert(response.request().postDataJSON().expected_draft_version === changed.draft_version, 'Delete uses explicitly reloaded Draft version')
  const authoritative = await response.json()
  assert(authoritative.draft_version === changed.draft_version + 1, 'Deletion advances saved Draft')
  await dialog.waitFor({ state: 'hidden' })
  assert(!await page.getByRole('heading', { name: 'codex-saved', exact: true }).isVisible(), 'Catalog shows authoritative deletion')
  await page.waitForFunction(() => document.activeElement?.textContent?.trim() === 'Refresh')
  assert(await page.getByRole('button', { name: 'Refresh', exact: true }).evaluate(element => element === document.activeElement), 'Deleted trigger restores focus to Refresh')
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Minimum desktop catalog has no page overflow')
  await page.screenshot({ path: 'dist/frontend-catalog/workers-codex-1024.png', animations: 'disabled' })
  assert(!(await api('/configuration/revisions/active')).configuration.workers.some(worker => worker.name === 'codex-saved'), 'Delete immediately removes Worker from current effective configuration')
  const historical = await api(`/configuration/revisions/${encodeURIComponent(revision.id)}`)
  assert(historical.configuration.workers.some(worker => worker.name === 'codex-saved'), 'Historical Worker configuration preserved')
  assert(historical.secret_references.some(reference => reference.worker_name === 'codex-saved'), 'Historical Worker Secret references preserved')

  // Existing Projects retain the historical catalog; new ones immediately bind the deletion.
  const nextRevision = await api('/configuration/revisions/active')
  assert((await api(`/projects/${project.project.id}`)).project.config_revision === revision.id, 'Existing Project retains original configuration after deletion')
  const newProject = await api('/projects', { title: 'New Worker revision acceptance', origin: 'New config', goal: 'Use latest revision', bootstrap_enabled: false }, 'post')
  assert(newProject.project.config_revision === nextRevision.id, 'New Project binds latest applied settings')
  await refresh()
  await page.getByText('No Codex Workers', { exact: true }).waitFor()
  await page.goto(`${origin}/workers/claude`)
  await card('claude-saved').getByText('Configured', { exact: true }).waitFor()
  await page.getByRole('link', { name: 'Pi', exact: true }).click()
  await card('pi-saved').getByText('Configured', { exact: true }).waitFor()
  for (const viewport of [{ width: 320, height: 568 }, { width: 768, height: 1024 }, { width: 844, height: 390 }]) {
    await page.setViewportSize(viewport)
    await page.getByRole('list', { name: 'Pi Workers', exact: true }).waitFor()
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `Workers fit ${viewport.width} × ${viewport.height}`)
    assert(await page.getByRole('link', { name: 'New Worker', exact: true }).isVisible(), 'Worker creation remains available')
  }
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.getByRole('list', { name: 'Pi Workers', exact: true }).waitFor()
  return { workerBranches: 'passed', savedReadiness: 'passed', keyboardConfirmation: 'passed', realDeletionAndConflict: 'passed', historicalProjectBinding: 'passed', secretMasking: 'passed', responsiveViewports: 'passed' }
}
