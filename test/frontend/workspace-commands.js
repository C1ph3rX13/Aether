async page => {
  const closeInspection = async () => {
    const panel = page.getByRole('dialog', { name: 'Project inspection', exact: true })
    if (await panel.isVisible()) { await page.keyboard.press('Escape'); await panel.waitFor({ state: 'hidden' }) }
  }
  const openInspection = async () => {
    if (!await page.getByRole('dialog', { name: 'Project inspection', exact: true }).isVisible())
      await page.getByRole('button', { name: 'Toggle right inspection panel', exact: true }).click()
  }
  const projectAction = async name => {
    await closeInspection()
    if (['Pause', 'Resume', 'Reopen', 'Snapshot'].includes(name)) {
      await page.getByRole('button', { name, exact: true }).click()
    } else {
      await page.getByRole('button', { name: 'Project actions', exact: true }).click()
      await page.getByRole('menuitem', { name: name === 'Delete' ? 'Delete Project' : name, exact: true }).click()
    }
  }
  const selectOption = async (trigger, label) => {
    await trigger.click()
    await page.getByRole('option', { name: label, exact: true }).click()
  }
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const origin = new URL(page.url()).origin
  const { id, finalFact } = await page.evaluate(() => JSON.parse(sessionStorage.getItem('workspace-acceptance')))
  const path = `/projects/${id}`
  const api = async (suffix, data, method = 'post') => {
    const response = await page.request[method](`${origin}/api/v1${suffix}`, data ? { data } : undefined)
    assert(response.ok(), `${method} ${suffix}: ${response.status()} ${await response.text()}`)
    return response.json()
  }
  const read = () => api(path, undefined, 'get')
  const live = () => page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  const lifecycle = async name => {
    await live()
    await projectAction(name)
    const dialog = page.getByRole('alertdialog', { name: `${name} Project`, exact: true })
    await dialog.getByRole('button', { name, exact: true }).click()
    await dialog.waitFor({ state: 'hidden' })
    await live()
  }
  const addHint = async content => {
    await live()
    await page.getByRole('button', { name: 'Add Hint', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Add Hint', exact: true })
    await dialog.getByLabel('Hint text', { exact: true }).fill(content)
    await dialog.getByRole('button', { name: 'Add Hint', exact: true }).click()
    await dialog.waitFor({ state: 'hidden' })
    await live()
  }
  await live()
  const pendingGuidance = `Pending Workspace guidance ${Date.now()}`
  // Paused Projects can receive guidance, while Completion remains unavailable.
  assert(await page.getByRole('button', { name: 'Complete Project', exact: true }).isDisabled(), 'Paused completion unavailable')
  await addHint('Paused Workspace guidance')
  assert((await read()).hints.some(hint => hint.content === 'Paused Workspace guidance' && hint.creator === 'catalog-operator'), 'Hint is attributed by the Server')
  await lifecycle('Resume')
  const graph = page.getByRole('application', { name: 'Blackboard graph', exact: true })
  assert(await graph.getByRole('checkbox', { name: 'Select f001 as Completion Evidence', exact: true }).count() === 0, 'Retracted Fact ineligible')
  assert(await graph.getByRole('checkbox', { name: 'Select goal as Completion Evidence', exact: true }).count() === 0, 'Goal ineligible')
  const evidence = graph.getByRole('checkbox', { name: `Select ${finalFact} as Completion Evidence`, exact: true })
  await evidence.focus()
  await page.keyboard.press('Space')
  assert(await evidence.evaluate(el => el === document.activeElement), 'Checkbox focus survives selection')
  const originEvidence = graph.getByRole('checkbox', { name: 'Select origin as Completion Evidence', exact: true })
  await originEvidence.focus()
  await originEvidence.check()
  await page.getByText('2 Facts selected as Completion Evidence', { exact: true }).waitFor()
  const intentButton = graph.getByRole('button', { name: 'Open Intent i001', exact: true })
  await intentButton.focus()
  await page.keyboard.press('Enter')
  await page.getByRole('region', { name: 'Record Detail', exact: true }).getByRole('heading', { name: 'Intent i001', exact: true }).waitFor()
  await closeInspection()
  await closeInspection()
  await page.getByText('2 Facts selected as Completion Evidence', { exact: true }).waitFor()

  await page.getByRole('button', { name: 'Complete Project', exact: true }).click()
  let dialog = page.getByRole('dialog', { name: 'Complete Project', exact: true })
  const facts = (await read()).facts
  assert(await dialog.getByText(facts.find(fact => fact.id === finalFact).description.trim(), { exact: true }).textContent() === facts.find(fact => fact.id === finalFact).description.trim(), 'Confirmation exposes complete long evidence')
  assert(await dialog.getByText('Known Origin evidence', { exact: true }).isVisible(), 'Hidden Origin appears in confirmation')
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await dialog.waitFor({ state: 'hidden' })
  assert(await page.getByRole('button', { name: 'Complete Project', exact: true }).evaluate(el => el === document.activeElement), 'Cancel restores Completion trigger focus')

  // A genuine concurrent Hint makes the already submitted Revision stale.
  const commandURL = `${origin}/api/v1${path}/actions/complete`
  await page.route(commandURL, async route => {
    const current = await read()
    await api(`${path}/hints`, { content: 'Concurrent Workspace guidance', expected_revision: current.project.revision })
    await route.continue()
  })
  await page.getByRole('button', { name: 'Complete Project', exact: true }).click()
  dialog = page.getByRole('dialog', { name: 'Complete Project', exact: true })
  await dialog.getByLabel('Completion description', { exact: true }).fill('Explicit Operator completion from two selected sources')
  const conflict = page.waitForResponse(response => response.url() === commandURL && response.status() === 409)
  await dialog.getByRole('button', { name: 'Complete Project', exact: true }).click()
  await conflict
  await page.unroute(commandURL)
  await dialog.getByRole('alert').getByText('This Project changed elsewhere. Review the refreshed state and retry.', { exact: true }).waitFor()
  assert(await dialog.getByLabel('Completion description', { exact: true }).inputValue() === 'Explicit Operator completion from two selected sources', 'Conflict preserves description')
  await live()
  assert(await dialog.getByText('2 selected Facts', { exact: true }).isVisible(), 'Conflict preserves valid evidence')
  await page.screenshot({ path: 'dist/frontend-catalog/workspace-completion-1440.png' })
  await page.setViewportSize({ width: 1024, height: 720 })
  assert(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth), 'Long confirmation fits minimum desktop')
  await page.screenshot({ path: 'dist/frontend-catalog/workspace-completion-1024.png' })
  await page.setViewportSize({ width: 1440, height: 900 })
  const completedResponse = page.waitForResponse(response => response.url() === commandURL && response.ok())
  await dialog.getByRole('button', { name: 'Complete Project', exact: true }).click()
  const completed = await (await completedResponse).json()
  await dialog.waitFor({ state: 'hidden' })
  await live()
  let detail = await read()
  assert(detail.project.status === 'completed', 'Completion lifecycle is authoritative')
  assert(JSON.stringify(completed.intent.from.slice().sort()) === JSON.stringify([finalFact, 'origin'].sort()), 'Explicit selected IDs are submitted')
  assert(completed.completion.actor === 'catalog-operator' && completed.intent.creator === 'catalog-operator', 'Completion uses Server Operator attribution')
  assert(!('operator' in (await completedResponse).request().postDataJSON()), 'No browser Operator override')
  assert(await page.getByRole('button', { name: 'Add Hint', exact: true }).isDisabled(), 'Completed Hint unavailable')
  await graph.getByRole('button', { name: `Open Intent ${completed.intent.id}`, exact: true }).focus()
  await page.keyboard.press('Enter')
  const sheet = page.getByRole('region', { name: 'Record Detail', exact: true })
  await sheet.getByRole('heading', { name: 'Completion Record', exact: true }).waitFor()
  assert(await sheet.getByRole('button', { name: 'Open Fact origin', exact: true }).isVisible(), 'Immutable Completion evidence is inspectable')
  assert(await sheet.getByRole('button', { name: `Open Fact ${finalFact}`, exact: true }).isVisible(), 'Both Completion sources persist')
  await page.getByRole('tab', { name: 'Log', exact: true }).click()
  await page.getByRole('region', { name: `Revision ${completed.project_revision}`, exact: true }).waitFor()

  await projectAction('Reopen')
  dialog = page.getByRole('dialog', { name: 'Reopen Project', exact: true })
  await dialog.getByLabel('Reason for reopening', { exact: true }).fill('Verify the next Generation')
  await dialog.getByRole('button', { name: 'Reopen', exact: true }).click()
  await dialog.waitFor({ state: 'hidden' })
  await live()
  detail = await read()
  assert(detail.project.generation === completed.project.generation + 1 && detail.project.status === 'active', 'Reopen starts next Generation')
  assert(detail.hints.some(hint => hint.content === 'Verify the next Generation' && hint.generation === detail.project.generation), 'Reopen feedback is current Hint')
  assert(detail.completion_records.some(record => record.intent_id === completed.intent.id), 'Reopen retains immutable Completion Record')
  await lifecycle('Pause')

  // Hold a genuine receipt while synchronization already observes the committed write.
  let release, started
  const held = new Promise(resolve => { release = resolve })
  const begun = new Promise(resolve => { started = resolve })
  const hintURL = `${origin}/api/v1${path}/hints`
  await page.route(hintURL, async route => {
    const response = await route.fetch()
    started()
    await held
    await route.fulfill({ response })
  })
  await page.getByRole('button', { name: 'Add Hint', exact: true }).click()
  dialog = page.getByRole('dialog', { name: 'Add Hint', exact: true })
  await dialog.getByLabel('Hint text', { exact: true }).fill(pendingGuidance)
  await dialog.getByRole('button', { name: 'Add Hint', exact: true }).click()
  await begun
  try {
    await page.keyboard.press('Escape')
    assert(await dialog.isVisible() && await dialog.getByRole('button', { name: 'Cancel', exact: true }).isDisabled(), 'Pending write retains confirmation')
    assert(await page.getByRole('button', { name: 'Project actions', exact: true, includeHidden: true }).isDisabled(), 'Pending Hint excludes the Project command menu')
    assert(await dialog.getByRole('button', { name: 'Adding…', exact: true }).isDisabled(), 'Pending Hint excludes repeated submit')
    assert(await page.getByRole('button', { name: 'New Project', exact: true, includeHidden: true }).isDisabled(), 'Pending Workspace write excludes global creation')
  } finally { release(); await page.unrouteAll({ behavior: 'wait' }) }
  await dialog.waitFor({ state: 'hidden' })
  await live()
  await openInspection()
  await page.getByRole('tab', { name: /^Hints/, exact: false }).click()
  await page.getByRole('tabpanel', { name: /^Hints/, exact: false }).getByText(pendingGuidance, { exact: true }).waitFor()

  await projectAction('Rename')
  dialog = page.getByRole('dialog', { name: 'Rename Project', exact: true })
  await dialog.getByLabel('Title', { exact: true }).fill('Workspace command acceptance')
  await dialog.getByRole('button', { name: 'Rename Project', exact: true }).click()
  await dialog.waitFor({ state: 'hidden' })
  await live()
  await page.getByRole('heading', { name: 'Workspace command acceptance', exact: true }).waitFor()
  await projectAction('Rename')
  dialog = page.getByRole('dialog', { name: 'Rename Project', exact: true })
  await dialog.getByLabel('Title', { exact: true }).fill('Workspace Inspection Acceptance')
  await dialog.getByRole('button', { name: 'Rename Project', exact: true }).click()
  await dialog.waitFor({ state: 'hidden' })
  await live()

  const snapshotRevision = (await read()).project.revision
  await projectAction('Snapshot')
  dialog = page.getByRole('dialog', { name: 'Snapshot Blackboard', exact: true })
  await selectOption(dialog.getByLabel('Format', { exact: true }), 'JSON')
  await dialog.getByRole('button', { name: 'Export', exact: true }).click()
  const snapshot = JSON.parse(await dialog.getByLabel('Export result', { exact: true }).inputValue())
  assert(snapshot.project.revision === snapshotRevision, 'Snapshot fixes the selected authoritative Revision')
  await page.keyboard.press('Escape')
  await dialog.waitFor({ state: 'hidden' })

  // Delete only an expendable isolated Project; the main fixture remains for recovery acceptance.
  const disposable = await api('/projects', { title: 'Workspace typed deletion', origin: 'Origin', goal: 'Goal', bootstrap_enabled: false })
  await page.goto(`${origin}/projects/${disposable.project.id}`)
  await live()
  await projectAction('Delete')
  dialog = page.getByRole('alertdialog', { name: 'Delete Project', exact: true })
  await dialog.getByLabel('Project title confirmation', { exact: true }).fill('wrong title')
  assert(await dialog.getByRole('button', { name: 'Delete Project', exact: true }).isDisabled(), 'Typed title must match')
  await dialog.getByLabel('Project title confirmation', { exact: true }).fill('Workspace typed deletion')
  await dialog.getByRole('button', { name: 'Delete Project', exact: true }).click()
  await page.waitForURL(`${origin}/projects`)
  assert((await page.request.get(`${origin}/api/v1/projects/${disposable.project.id}`)).status() === 404, 'Workspace deletion is authoritative')
  await page.goto(`${origin}${path}`)
  await live()
  assert(await page.getByRole('checkbox', { checked: true }).count() === 0, 'Completion Evidence is cleared')
  await page.setViewportSize({ width: 1024, height: 720 })
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Workspace actions fit minimum desktop')
  await page.screenshot({ path: 'dist/frontend-catalog/workspace-actions-1024.png', fullPage: true })
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.screenshot({ path: 'dist/frontend-catalog/workspace-actions-1440.png', fullPage: true })
  return { workspaceCommands: 'passed', completionIntent: completed.intent.id, selectedEvidence: completed.intent.from }
}
