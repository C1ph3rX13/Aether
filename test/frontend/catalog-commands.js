async page => {
  const selectOption = async (trigger, label) => {
    await trigger.click()
    await page.getByRole('option', { name: label, exact: true }).click()
  }
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const origin = new URL(page.url()).origin
  const api = async (path, data, method = 'post') => {
    const response = await page.request[method](`${origin}/api/v1${path}`, data ? { data } : undefined)
    assert(response.ok(), `${method} ${path}: ${response.status()} ${await response.text()}`)
    return response.json()
  }
  const refresh = async () => {
    const read = page.waitForResponse(response => response.url().endsWith('/api/v1/projects') && response.ok())
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await read
    await page.getByRole('button', { name: 'Refresh', exact: true }).waitFor({ state: 'visible' })
  }
  const action = async (title, name) => {
    await page.getByRole('button', { name: `Actions for ${title}`, exact: true }).click()
    await page.getByRole('menuitem', { name, exact: true }).click()
  }
  const lifecycle = async (title, name) => {
    await action(title, name)
    const dialog = page.getByRole('alertdialog', { name: `${name} Project`, exact: true })
    await dialog.getByRole('button', { name, exact: true }).click()
    await dialog.waitFor({ state: 'hidden' })
  }
  const holdResponse = async pattern => {
    let release, signalStarted
    const held = new Promise(resolve => { release = resolve })
    const started = new Promise(resolve => { signalStarted = resolve })
    await page.route(pattern, async route => {
      if (route.request().method() !== 'POST') return route.continue()
      const response = await route.fetch()
      signalStarted()
      await held
      await route.fulfill({ response })
    })
    return { started, finish: async () => { release(); await page.unrouteAll({ behavior: 'wait' }) } }
  }

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.getByRole('button', { name: 'New Project', exact: true }).click()
  const creation = page.getByRole('dialog', { name: 'Create Project', exact: true })
  assert(await creation.getByRole('checkbox', { name: 'Bootstrap', exact: true }).isChecked(), 'Bootstrap defaults on')
  await creation.getByRole('button', { name: 'Create Project', exact: true }).click()
  await creation.getByText('Title is required.', { exact: true }).waitFor()
  const title = 'Operator Catalog demonstration'
  await creation.getByLabel('Title', { exact: true }).fill(title)
  await creation.getByLabel('Origin', { exact: true }).fill('Known Catalog evidence')
  await creation.getByLabel('Goal', { exact: true }).fill('Complete the operator workflow')
  await creation.getByRole('button', { name: 'Add Hint', exact: true }).click()
  await creation.getByLabel('Hint 1', { exact: true }).fill('Initial operator guidance')
  const createdResponse = page.waitForResponse(response => response.url().endsWith('/api/v1/projects') && response.request().method() === 'POST')
  const heldCreation = await holdResponse('**/api/v1/projects')
  try {
    await creation.getByRole('button', { name: 'Create Project', exact: true }).click()
    await heldCreation.started
    await page.keyboard.press('Escape')
    assert(await creation.isVisible() && await creation.getByRole('button', { name: 'Cancel', exact: true }).isDisabled(), 'Pending creation blocks dismissal')
    assert(await creation.getByRole('button', { name: 'Creating...', exact: true }).isDisabled(), 'Pending creation blocks duplicate submission')
  } finally { await heldCreation.finish() }
  const created = await (await createdResponse).json()
  const id = created.project.id
  await page.waitForURL(`${origin}/projects/${id}`)
  await page.getByRole('heading', { name: title, exact: true }).waitFor()
  await page.getByRole('application', { name: 'Blackboard graph', exact: true }).waitFor()
  assert(created.hints[0].creator === 'catalog-operator', 'Initial Hint identity is server-owned')
  assert(created.project.bootstrap_enabled && created.intents.some(intent => intent.kind === 'bootstrap'), 'Default Bootstrap is persisted')
  assert(!('operator' in (await createdResponse).request().postDataJSON()), 'Browser does not submit Operator Identity')
  await page.getByRole('link', { name: 'Projects', exact: true }).first().click()
  await page.getByLabel('Search Projects', { exact: true }).fill(title)
  const heldPause = await holdResponse('**/actions/pause')
  const pauseDialog = page.getByRole('alertdialog', { name: 'Pause Project', exact: true })
  try {
    await action(title, 'Pause')
    await pauseDialog.getByRole('button', { name: 'Pause', exact: true }).click()
    await heldPause.started
    assert(await page.getByRole('button', { name: 'New Project', exact: true, includeHidden: true }).isDisabled(), 'Pending lifecycle excludes global creation')
    assert(await page.locator('button').filter({ hasText: /^Pause Active$/ }).evaluate(button => button.disabled), 'An individual write excludes bulk commands')
    await page.keyboard.press('Escape')
    assert(await pauseDialog.isVisible() && await pauseDialog.getByRole('button', { name: 'Cancel', exact: true }).isDisabled(), 'Pending lifecycle confirmation cannot be dismissed')
  } finally { await heldPause.finish() }
  await pauseDialog.waitFor({ state: 'hidden' })
  assert((await api(`/projects/${id}`, undefined, 'get')).project.status === 'paused', 'Pause is authoritative')
  await lifecycle(title, 'Resume')
  let detail = await api(`/projects/${id}`, undefined, 'get')
  assert(detail.project.status === 'active', 'Resume is authoritative')

  // A real concurrent write advances the Revision after confirmation opens.
  await action(title, 'Rename')
  const rename = page.getByRole('dialog', { name: 'Rename Project', exact: true })
  await rename.getByLabel('Title', { exact: true }).fill('Operator renamed Project')
  await api(`/projects/${id}/actions/rename`, { title: 'Concurrent Catalog title', expected_revision: detail.project.revision })
  const conflictResponse = page.waitForResponse(response => response.url().endsWith(`/projects/${id}/actions/rename`) && response.status() === 409)
  await rename.getByRole('button', { name: 'Rename Project', exact: true }).click()
  const conflict = await conflictResponse
  assert(conflict.request().postDataJSON().expected_revision === detail.project.revision, 'Rename retains the captured server Revision')
  await rename.getByRole('alert').getByText('This Project changed elsewhere. Review the refreshed state and retry.', { exact: true }).waitFor()
  assert(await rename.getByLabel('Title', { exact: true }).inputValue() === 'Operator renamed Project', 'Conflict keeps the failed edit')
  assert((await api(`/projects/${id}`, undefined, 'get')).project.title === 'Concurrent Catalog title', 'Conflict does not silently overwrite')
  await rename.getByRole('button', { name: 'Rename Project', exact: true }).click()
  await rename.waitFor({ state: 'hidden' })
  const renamed = 'Operator renamed Project'
  await page.getByLabel('Search Projects', { exact: true }).fill(renamed)
  detail = await api(`/projects/${id}`, undefined, 'get')
  assert(detail.project.title === renamed, 'Explicit retry uses the refreshed Revision')

  await api(`/projects/${id}/actions/complete`, { expected_revision: detail.project.revision, from: ['origin'], description: 'Catalog acceptance completion' })
  await refresh()
  await action(renamed, 'Reopen')
  const reopen = page.getByRole('dialog', { name: 'Reopen Project', exact: true })
  assert(await reopen.getByLabel('Reason for reopening', { exact: true }).evaluate(element => element.required && element.getAttribute('aria-describedby')?.split(/\s+/).some(id => document.getElementById(id)?.textContent.includes('Required. Your reason becomes the first Hint'))), 'Reopen reason exposes its required state and guidance')
  await reopen.getByLabel('Reason for reopening', { exact: true }).fill('Inspect the next Generation')
  await reopen.getByRole('button', { name: 'Reopen', exact: true }).click()
  await reopen.waitFor({ state: 'hidden' })
  detail = await api(`/projects/${id}`, undefined, 'get')
  assert(detail.project.status === 'active' && detail.project.generation === 2, 'Reopen advances the authoritative Generation')
  assert(detail.hints.some(hint => hint.content === 'Inspect the next Generation' && hint.creator === 'catalog-operator'), 'Reopen feedback becomes an attributed Hint')

  await action(renamed, 'Export Project')
  const exported = page.getByRole('dialog', { name: 'Export Project', exact: true })
  const latestTitle = 'Latest authoritative export title'
  await api(`/projects/${id}/actions/rename`, { title: latestTitle, expected_revision: detail.project.revision })
  await selectOption(exported.getByLabel('Format', { exact: true }), 'JSON')
  await exported.getByRole('button', { name: 'Export', exact: true }).click()
  const output = exported.getByLabel('Export result', { exact: true })
  await output.waitFor()
  assert(JSON.parse(await output.inputValue()).project.title === latestTitle, 'Export reads latest server state rather than the Catalog Revision')
  const downloadEvent = page.waitForEvent('download')
  await exported.getByRole('button', { name: 'Download File', exact: true }).click()
  const download = await downloadEvent
  const stream = await download.createReadStream()
  let contents = ''
  for await (const chunk of stream) contents += chunk.toString()
  assert(JSON.parse(contents).project.title === latestTitle && download.suggestedFilename().endsWith('.json'), 'Downloaded JSON matches the authoritative response')
  await selectOption(exported.getByLabel('Format', { exact: true }), 'YAML')
  await exported.getByRole('button', { name: 'Export', exact: true }).click()
  await output.waitFor()
  assert((await output.inputValue()).includes(latestTitle), 'YAML export is available')
  await page.keyboard.press('Escape')
  await page.getByLabel('Search Projects', { exact: true }).fill(latestTitle)
  await refresh()

  await action(latestTitle, 'Delete Project')
  const deletion = page.getByRole('alertdialog', { name: 'Delete Project', exact: true })
  await deletion.getByLabel('Project title confirmation', { exact: true }).fill(latestTitle)
  await deletion.getByRole('button', { name: 'Cancel', exact: true }).click()
  await page.waitForFunction(title => document.activeElement?.getAttribute('aria-label') === `Actions for ${title}`, latestTitle)
  assert(await page.getByRole('button', { name: `Actions for ${latestTitle}`, exact: true }).evaluate(element => element === document.activeElement), 'Cancel returns focus to the row action trigger')
  await action(latestTitle, 'Delete Project')
  assert(await deletion.getByLabel('Project title confirmation', { exact: true }).inputValue() === '', 'Reopening requires fresh title confirmation')
  await deletion.getByLabel('Project title confirmation', { exact: true }).fill(renamed)
  assert(await deletion.getByRole('button', { name: 'Delete Project', exact: true }).isDisabled(), 'Deletion requires the current title')
  await deletion.getByLabel('Project title confirmation', { exact: true }).fill(latestTitle)
  await deletion.getByRole('button', { name: 'Delete Project', exact: true }).click()
  await deletion.waitFor({ state: 'hidden' })
  assert((await page.request.get(`${origin}/api/v1/projects/${id}`)).status() === 404, 'Deletion removes the Project through the real API')
  await page.getByText('No matching Projects', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Clear filters', exact: true }).click()

  const originals = await api('/projects', undefined, 'get')
  const bulkIds = []
  for (let index = 0; index < 12; index++) {
    const project = await api('/projects', { title: `Bulk scope ${index + 1}`, origin: 'Known evidence', goal: 'Full Catalog bulk scope', bootstrap_enabled: false })
    assert(project.project.bootstrap_enabled === false && project.intents.length === 0, 'Explicit Bootstrap opt-out remains supported')
    bulkIds.push(project.project.id)
  }
  await refresh()
  await page.getByLabel('Search Projects', { exact: true }).fill('Bulk scope')
  await selectOption(page.getByLabel('Lifecycle', { exact: true }), 'Active')
  await page.getByRole('button', { name: 'Go to last page', exact: true }).click()
  await page.getByText('Page 2 of 2', { exact: true }).waitFor()
  const active = (await api('/projects', undefined, 'get')).filter(entry => entry.project.status === 'active')
  // Delay delivery of genuine Go responses; no synthetic success or test endpoint.
  const heldBulk = await holdResponse('**/actions/pause')
  try {
    await page.getByRole('button', { name: 'Pause Active', exact: true }).click()
    const bulk = page.getByRole('alertdialog', { name: 'Pause Active Projects', exact: true })
    assert((await bulk.innerText()).includes(`all ${active.length} Active Projects`), 'Bulk confirmation includes hidden Active Projects')
    const concurrent = active.find(entry => entry.project.id === bulkIds[0]).project
    await api(`/projects/${concurrent.id}/actions/rename`, { title: 'Bulk scope concurrent title', expected_revision: concurrent.revision })
    await bulk.getByRole('button', { name: 'Pause Active', exact: true }).click()
    await heldBulk.started
    assert(await page.getByRole('button', { name: 'Pausing…', exact: true }).isDisabled(), 'Pending bulk excludes duplicate submission')
    assert(await page.getByRole('button', { name: 'New Project', exact: true }).isDisabled(), 'Pending bulk excludes Project creation')
    await page.getByLabel('Search Projects', { exact: true }).fill('')
    await selectOption(page.getByLabel('Lifecycle', { exact: true }), 'All lifecycles')
    await page.getByRole('button', { name: `Actions for ${originals[0].project.title}`, exact: true }).click()
    for (const name of ['Rename', 'Delete Project']) assert(await page.getByRole('menuitem', { name, exact: true }).getAttribute('aria-disabled') === 'true', `Bulk excludes ${name}`)
    assert(await page.getByRole('menuitem', { name: 'Export Project', exact: true }).getAttribute('aria-disabled') !== 'true', 'Export remains available during bulk writes')
    await page.getByRole('menuitem', { name: 'Export Project', exact: true }).click()
    await page.getByRole('dialog', { name: 'Export Project', exact: true }).getByRole('button', { name: 'Export', exact: true }).click()
    await page.getByLabel('Export result', { exact: true }).waitFor()
    await page.keyboard.press('Escape')
  } finally { await heldBulk.finish() }
  await page.getByText(`Paused ${active.length - 1} Projects. Could not pause 1; review the refreshed Catalog and retry.`, { exact: true }).waitFor()
  let afterBulk = await api('/projects', undefined, 'get')
  assert(afterBulk.filter(entry => entry.project.status === 'active').length === 1, 'Partial bulk conflict preserves the concurrent Project without blind retry')
  await page.getByRole('button', { name: 'Pause Active', exact: true }).click()
  const retryBulk = page.getByRole('alertdialog', { name: 'Pause Active Projects', exact: true })
  assert((await retryBulk.innerText()).includes('all 1 Active Projects'), 'Explicit bulk recovery confirms a fresh authoritative scope')
  await retryBulk.getByRole('button', { name: 'Pause Active', exact: true }).click()
  await page.getByText('Paused 1 active Project.', { exact: true }).waitFor()
  afterBulk = await api('/projects', undefined, 'get')
  assert(afterBulk.every(entry => entry.project.status !== 'active'), 'Pause Active acts over the full Catalog across search, filters and pagination')
  assert(await page.getByLabel('Active: 0', { exact: true }).isVisible(), 'Statistics refresh from authoritative responses')

  for (const entry of afterBulk) {
    if (bulkIds.includes(entry.project.id)) await api(`/projects/${entry.project.id}/actions/delete`, { expected_title: entry.project.title, expected_revision: entry.project.revision })
    else if (originals.find(original => original.project.id === entry.project.id)?.project.status === 'active') await api(`/projects/${entry.project.id}/actions/resume`, { expected_revision: entry.project.revision })
  }
  await refresh()
  await page.getByText('9 of 9 Projects', { exact: true }).waitFor()
  return { createToWorkspace: 'passed', lifecycle: 'passed', renameConflict: 'passed', authoritativeExport: 'passed', typedDeletion: 'passed', fullCatalogBulk: 'passed', commandExclusion: 'passed' }
}
