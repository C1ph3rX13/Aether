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
  const { id } = await page.evaluate(() => JSON.parse(sessionStorage.getItem('workspace-acceptance')))
  const path = `/projects/${id}`
  const api = async (suffix, data, method = 'get') => {
    const response = await page.request[method](`${origin}/api/v1${suffix}`, data ? { data } : undefined)
    assert(response.ok(), `${method} ${suffix}: HTTP ${response.status()}`)
    return response.json()
  }
  const live = () => page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  const controls = page.getByRole('region', { name: 'Replay controls', exact: true })
  const frame = () => controls.getByLabel('Go to Replay frame', { exact: true })
  const frameCount = async () => {
    await closeInspection()
    await frame().click()
    const count = await page.getByRole('listbox').getByRole('option').count()
    await page.keyboard.press('Escape')
    return count
  }
  const select = async revision => {
    await closeInspection()
    await selectOption(frame(), `R${revision}`)
    await controls.getByRole('status').filter({ hasText: `Frame ${revision} of` }).waitFor()
  }
  await page.goto(`${origin}${path}`)
  await live()
  const before = await api(path)
  const frozenTo = before.project.revision
  assert(frozenTo > 100, 'Seeded history crosses a Log page boundary')
  const first = await api(`${path}/history/1/blackboard`)
  const second = await api(`${path}/history/2/blackboard`)
  const corrected = await api(`${path}/history/3/blackboard`)
  assert(second.facts.some(fact => fact.id === 'f001'), 'History includes committed Bootstrap Fact')
  await projectAction('Replay')
  await controls.getByRole('button', { name: 'Pause Replay', exact: true }).waitFor()
  await page.waitForFunction(() => Number(document.getElementById('replay-frame').textContent.slice(1)) > 1)
  await select(1)
  const log = page.getByRole('region', { name: 'Project Log', exact: true })
  await openInspection()
  await page.getByRole('tab', { name: 'Log', exact: true }).click()
  await log.getByRole('region', { name: 'Revision 1', exact: true }).waitFor()
  assert(await log.getByRole('region', { name: /^Revision \d+$/ }).count() === 1, 'Log displays only the first complete atomic group')
  await page.getByRole('tab', { name: 'Details', exact: true }).click()
  await closeInspection()
  assert(await page.locator('.react-flow__node').count() === first.facts.length + first.intents.length, 'Facts read authoritative first Snapshot')
  assert(await page.getByRole('checkbox', { name: 'Select origin as Completion Evidence', exact: true }).isDisabled(), 'Historical origin is not Completion Evidence')
  await select(2)
  await page.getByRole('application', { name: 'Blackboard graph', exact: true }).getByRole('button', { name: 'Open Fact f001', exact: true }).focus()
  await page.keyboard.press('Enter')
  let sheet = page.getByRole('region', { name: 'Record Detail', exact: true })
  await sheet.getByText(second.facts.find(fact => fact.id === 'f001').description.trim(), { exact: true }).waitFor()
  assert(await sheet.getByText('Retraction Reason', { exact: true }).count() === 0, 'Historical Fact has not yet been retracted')
  await sheet.getByRole('button', { name: 'Open Intent i001', exact: true }).click()
  sheet = page.getByRole('region', { name: 'Record Detail', exact: true })
  await sheet.getByText('Current Execution Evidence is unavailable in historical Replay.', { exact: true }).waitFor()
  assert(await sheet.getByRole('button', { name: 'Open Fact f001', exact: true }).isVisible(), 'Historical To relationship comes from the frame')
  await select(3)
  await page.getByRole('application', { name: 'Blackboard graph', exact: true }).getByRole('button', { name: 'Open Fact f001', exact: true }).focus()
  await page.keyboard.press('Enter')
  sheet = page.getByRole('region', { name: 'Record Detail', exact: true })
  await sheet.getByRole('button', { name: 'Retraction information', exact: true }).click()
  await sheet.getByText(corrected.facts.find(fact => fact.id === 'f001').retraction_reason, { exact: true }).waitFor()
  await sheet.getByRole('button', { name: 'Show in Log', exact: true }).click()
  const group = log.getByRole('region', { name: 'Revision 3', exact: true })
  await group.waitFor()
  const correctionLog = await api(`${path}/log?before_revision=4&limit=1`)
  assert(await group.getByRole('listitem').count() === correctionLog.revisions[0].events.length, 'Supersede preserves the authoritative atomic group')

  // A genuine new Hint advances current Revision without changing the historical frame.
  const hint = await api(`${path}/hints`, { content: 'Live Hint outside frozen Replay', expected_revision: frozenTo }, 'post')
  await closeInspection()
  await controls.getByText('1 update pending', { exact: true }).waitFor()
  assert(await frame().textContent() === 'R3', 'Live update keeps the selected frame')
  assert(await frameCount() === frozenTo, 'New live Revision does not extend the window')
  await openInspection()
  await page.getByRole('tab', { name: /^Hints/, exact: false }).click()
  assert(await page.getByRole('tabpanel', { name: /^Hints/, exact: false }).getByText('Live Hint outside frozen Replay', { exact: true }).count() === 0, 'Live Hint stays outside historical tables')
  await closeInspection()
  for (const name of ['Add Hint', 'Complete Project'])
    assert(await page.getByRole('button', { name, exact: true }).isDisabled(), `${name} remains read-only`)
  await page.getByRole('button', { name: 'Project actions', exact: true }).click()
  for (const name of ['Rename', 'Delete Project'])
    assert(await page.getByRole('menuitem', { name, exact: true }).getAttribute('aria-disabled') === 'true', `${name} remains read-only`)
  await page.keyboard.press('Escape')
  await projectAction('Snapshot')
  let dialog = page.getByRole('dialog', { name: 'Snapshot Blackboard', exact: true })
  await selectOption(dialog.getByLabel('Format', { exact: true }), 'JSON')
  await dialog.getByRole('button', { name: 'Export', exact: true }).click()
  const exported = JSON.parse(await dialog.getByLabel('Export result', { exact: true }).inputValue())
  assert(JSON.stringify(exported) === JSON.stringify(corrected), 'Historical Snapshot is the authoritative displayed Revision')
  const downloadReady = page.waitForEvent('download')
  await dialog.getByRole('button', { name: 'Download File', exact: true }).click()
  const download = await downloadReady
  assert(download.suggestedFilename() === `${id}-r3.json`, 'Historical download retains its Revision')
  await download.saveAs('dist/frontend-catalog/replay-r3.json')
  await page.keyboard.press('Escape')
  await dialog.waitFor({ state: 'hidden' })
  await page.evaluate(() => window.scrollTo(0, 0))
  await page.screenshot({ path: 'dist/frontend-catalog/replay-frozen-1440.png' })
  await page.setViewportSize({ width: 1024, height: 720 })
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Replay fits the minimum desktop')
  await page.screenshot({ path: 'dist/frontend-catalog/replay-frozen-1024.png' })
  await page.setViewportSize({ width: 1440, height: 900 })

  // Hold the genuine successful Snapshot response until a real live update happens during Restart.
  let release, started
  const held = new Promise(resolve => { release = resolve })
  const begun = new Promise(resolve => { started = resolve })
  const heldURL = `${origin}/api/v1${path}/history/2/blackboard`
  await page.route(heldURL, async route => {
    const response = await route.fetch()
    started()
    await held
    await route.fulfill({ response })
  })
  await controls.getByRole('button', { name: 'Restart with Latest', exact: true }).click()
  await begun
  assert(await frame().isDisabled() && await frame().textContent() === 'R3', 'Restart retains the previous frozen view until ready')
  const duringRestart = await api(`${path}/hints`, { content: 'Live Hint after Restart capture', expected_revision: hint.project_revision }, 'post')
  try { await controls.getByText('2 updates pending', { exact: true }).waitFor() }
  finally { release(); await page.unrouteAll({ behavior: 'wait' }) }
  await controls.getByRole('button', { name: 'Pause Replay', exact: true }).waitFor()
  await select(1)
  assert(await frameCount() === hint.project_revision, 'Restart freezes its explicitly refreshed upper boundary')
  await closeInspection()
  await controls.getByText('1 update pending', { exact: true }).waitFor()
  await select(hint.project_revision)
  assert(await page.getByRole('button', { name: 'Add Hint', exact: true }).isDisabled(), 'Final historical frame stays read-only')
  await openInspection()
  await page.getByRole('tabpanel', { name: /^Hints/, exact: false }).getByText('Live Hint outside frozen Replay', { exact: true }).waitFor()
  await closeInspection()

  // Fault injection is at the browser HTTP boundary; recovery returns to the real API.
  await page.route(heldURL, route => route.abort('failed'))
  await controls.getByRole('button', { name: 'Restart with Latest', exact: true }).click()
  await page.getByText('Could not restart Replay. The frozen view remains readable. Retry Restart with Latest.', { exact: true }).waitFor()
  assert(await frame().textContent() === `R${hint.project_revision}`, 'Failed Snapshot preserves the displayed valid frame')
  assert(await frameCount() === hint.project_revision, 'Failed Restart preserves its full old window')
  await page.unroute(heldURL)
  await controls.getByRole('button', { name: 'Restart with Latest', exact: true }).click()
  await controls.getByRole('button', { name: 'Pause Replay', exact: true }).waitFor()
  await select(duringRestart.project_revision)
  assert(await frameCount() === duringRestart.project_revision, 'Explicit retry adopts the latest authoritative window')
  await controls.getByRole('button', { name: 'Exit Replay', exact: true }).click()
  await live()
  assert(await page.getByRole('button', { name: 'Add Hint', exact: true }).isEnabled(), 'Exit waits for synchronization before enabling writes')
  await openInspection()
  await page.getByRole('tabpanel', { name: /^Hints/, exact: false }).getByText('Live Hint after Restart capture', { exact: true }).waitFor()
  await closeInspection()
  // Start from retained stale data after a failed canonical read and an unseen real update.
  const readURL = `${origin}/api/v1${path}`
  await page.route(readURL, route => route.abort('failed'))
  await projectAction('Refresh Project')
  await page.getByText('Project update unavailable', { exact: true }).waitFor()
  const unseen = await api(`${path}/hints`, { content: 'Unseen Hint before stale Replay start', expected_revision: duringRestart.project_revision }, 'post')
  await page.unroute(readURL)
  await projectAction('Replay')
  await controls.getByRole('button', { name: 'Pause Replay', exact: true }).waitFor()
  await select(unseen.project_revision)
  assert(await frameCount() === unseen.project_revision, 'Starting from stale cache reads the authoritative current upper boundary')
  await controls.getByRole('button', { name: 'Exit Replay', exact: true }).click()
  await live()
  await openInspection()
  await page.getByRole('tab', { name: /^Hints/, exact: false }).click()
  await page.getByRole('tab', { name: 'Details', exact: true }).click()
  await closeInspection()
  await page.evaluate(() => window.scrollTo(0, 0))
  return { replay: 'passed', frozenTo, restartedTo: duringRestart.project_revision, staleStartTo: unseen.project_revision, historicalSnapshot: 3 }
}
