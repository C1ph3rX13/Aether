async page => {
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  await page.setViewportSize({ width: 1440, height: 900 })
  const origin = new URL(page.url()).origin
  const api = async (path, data, method = 'post') => {
    const response = await page.request[method](`${origin}/api/v1${path}`, data ? { data } : undefined)
    assert(response.ok(), `${method} ${path}: HTTP ${response.status()} ${await response.text()}`)
    return response.json()
  }
  const id = await page.evaluate(() => sessionStorage.getItem('workspace-acceptance-id'))
  const path = `/projects/${id}`
  let detail
  const deadline = Date.now() + 60000
  do {
    detail = await api(path, undefined, 'get')
    if (detail.facts.some(fact => fact.id === 'f001')) break
    if (Date.now() > deadline) throw new Error(`Trusted Dispatcher did not commit Bootstrap evidence: ${JSON.stringify({ detail, evidence: await api(`${path}/execution-evidence?intent_id=i001`, undefined, 'get'), readiness: await (await page.request.get(`${origin}/api/v1/ready`)).json() })}`)
    await new Promise(resolve => setTimeout(resolve, 250))
  } while (true)
  const execution = await api(`${path}/execution-evidence?intent_id=i001`, undefined, 'get')
  assert(execution.selected_intent_run?.status === 'committed', 'Bootstrap evidence is a real committed Run')
  let revision = detail.project.revision
  let original = 'f001'
  const longDescription = '完整记录 — épreuve — 検証\n' + 'Verified multilingual evidence. '.repeat(100)
  for (let index = 0; index < 12; index++) {
    const replacement = await api(`${path}/facts/${original}/actions/supersede`, { description: index === 11 ? longDescription : `Corrected Fact ${index + 1}`, reason: `Correction evidence ${index + 1}`, expected_revision: revision })
    original = replacement.replacement.id
    revision = replacement.project_revision
  }
  for (let index = 0; index < 12; index++) {
    const complete = await api(`${path}/actions/complete`, { from: ['origin'], description: `Completion evidence ${index + 1}`, expected_revision: revision })
    revision = complete.project.revision
    const reopened = await api(`${path}/actions/reopen`, { feedback: `Generation ${index + 2} guidance`, expected_revision: revision })
    revision = reopened.project.revision
  }
  for (let index = 0; index < 20; index++) {
    const hint = await api(`${path}/hints`, { content: `Historical Hint ${index + 1}`, expected_revision: revision })
    revision = hint.project_revision
  }
  await api(`${path}/actions/pause`, { expected_revision: revision })
  detail = await api(path, undefined, 'get')
  assert(detail.facts.length === 15 && detail.intents.length === 13, 'Complete generated Blackboard fixture')
  await page.evaluate(({ id, finalFact }) => sessionStorage.setItem('workspace-acceptance', JSON.stringify({ id, finalFact })), { id, finalFact: original })
  await page.goto(`${origin}/projects`)
  await page.getByLabel('Search Projects').fill('Workspace Inspection Acceptance')
  await page.getByRole('link', { name: 'Workspace Inspection Acceptance', exact: true }).click()
  await page.getByRole('heading', { name: 'Workspace Inspection Acceptance', exact: true }).waitFor()
  await page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  const drawer = page.getByRole('dialog', { name: 'Project inspection', exact: true })
  assert(!await drawer.isVisible(), 'C inspection defaults closed')
  const graph = page.getByRole('application', { name: 'Blackboard graph', exact: true })
  const inspector = page.getByRole('region', { name: 'Record Detail', exact: true })
  const close = async () => { if (await drawer.isVisible()) { await page.keyboard.press('Escape'); await drawer.waitFor({ state: 'hidden' }) } }
  const toggle = page.getByRole('button', { name: 'Toggle right inspection panel', exact: true })
  await toggle.click()
  assert(await page.getByRole('tablist', { name: 'Workspace inspection tabs', exact: true }).getByRole('tab').count() === 3, 'Three native inspection tabs')
  await page.waitForFunction(() => Math.abs(document.getElementById('project-inspection').getBoundingClientRect().right - innerWidth) < 0.1)
  const bounds = await drawer.boundingBox()
  assert(bounds.width === 448 && bounds.x + bounds.width === 1440, 'Native Sheet is on the right')
  await close()
  assert(await toggle.evaluate(el => el === document.activeElement), 'Closing restores inspection trigger focus')
  const open = async name => { await close(); const target = graph.getByRole('button', { name, exact: true }); await target.focus(); await page.keyboard.press('Enter') }
  await graph.getByRole('button', { name: 'Re-layout graph', exact: true }).click()
  assert((await api(path, undefined, 'get')).project.revision === detail.project.revision, 'Re-layout changes only presentation')
  await open('Open Fact f001')
  await inspector.getByText('Correction evidence 1', { exact: true }).waitFor()
  await inspector.getByRole('button', { name: 'Retraction information', exact: true }).click()
  assert(await inspector.getByText('catalog-operator', { exact: true }).isVisible(), 'Retraction actor comes from the server')
  await inspector.getByRole('button', { name: 'Open Intent i001', exact: true }).click()
  await inspector.getByRole('button', { name: 'Execution Evidence', exact: true }).click()
  await inspector.getByText(execution.selected_intent_run.run_id, { exact: true }).waitFor()
  assert(await inspector.getByText('File', { exact: true }).isVisible(), 'Evidence comes from the real Pi result file')
  assert(await inspector.getByText('No Active Reason', { exact: true }).isVisible(), 'Authoritative absence of Active Reason')
  await inspector.getByRole('button', { name: 'Open Fact origin', exact: true }).click()
  await inspector.getByRole('heading', { name: 'Fact origin', exact: true }).waitFor()
  await close()
  assert(await graph.locator('.react-flow__node').count() === detail.facts.length + detail.intents.length, 'Graph contains all Aether records')
  const expectedEdges = detail.intents.reduce((count, intent) => count + intent.from.length + (intent.to ? 1 : 0), 0) + detail.facts.filter(fact => fact.supersedes).length
  assert(await graph.locator('.react-flow__edge').count() === expectedEdges, 'Graph renders every source, produced Fact and correction relationship')
  await open(`Open Fact ${original}`)
  assert(await inspector.getByText(longDescription.trim(), { exact: true }).textContent() === longDescription.trim(), 'Complete long multilingual description')
  await close()
  await toggle.click()
  await inspector.getByRole('heading', { name: `Fact ${original}`, exact: true }).waitFor()
  await page.getByRole('tab', { name: /^Hints/, exact: false }).click()
  await page.getByRole('button', { name: 'Open Hint h001', exact: true }).click()
  await inspector.getByText('Follow the current evidence.', { exact: true }).waitFor()
  await page.getByRole('tab', { name: 'Log', exact: true }).click()
  const log = page.getByRole('region', { name: 'Project Log', exact: true })
  await log.getByRole('button', { name: 'Load older Log', exact: true }).click()
  await log.getByRole('region', { name: 'Revision 1', exact: true }).waitFor()
  assert(await log.getByRole('region', { name: 'Revision 2', exact: true }).getByText('Run · workspace-bootstrap', { exact: true }).isVisible(), 'Log preserves Run source and atomic Revision')
  await log.getByRole('button', { name: 'Inspect Intent i001 from Log', exact: true }).last().click()
  await inspector.getByRole('heading', { name: 'Intent i001', exact: true }).waitFor()
  await page.getByRole('tab', { name: 'Log', exact: true }).click()
  assert(await log.getByRole('region', { name: 'Revision 1', exact: true }).isVisible(), 'Inspection retains loaded Log context')
  revision = detail.project.revision
  for (let index = 0; index < 55; index++) {
    const hint = await api(`${path}/hints`, { content: `Live History rollover ${index + 1}`, expected_revision: revision })
    revision = hint.project_revision
  }
  await log.getByRole('region', { name: `Revision ${revision}`, exact: true }).waitFor()
  assert(await log.getByRole('region', { name: `Revision ${detail.project.revision}`, exact: true }).isVisible(), 'Previously loaded newest History survives window rollover')
  assert(await log.getByRole('region', { name: 'Revision 1', exact: true }).isVisible(), 'Older History survives live rollover')
  while (await log.getByRole('button', { name: 'Load older Log', exact: true }).count()) {
    const loader = log.getByRole('button', { name: 'Load older Log', exact: true })
    await loader.click()
    await log.getByRole('button', { name: 'Loading older Log…', exact: true }).waitFor({ state: 'hidden' })
  }
  assert(await log.getByRole('region', { name: /^Revision \d+$/ }).count() === revision, 'Every atomic Revision remains reachable after newest-page rollover')
  await page.reload()
  await page.getByRole('heading', { name: 'Workspace Inspection Acceptance', exact: true }).waitFor()
  await page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  await page.setViewportSize({ width: 1024, height: 720 })
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Workspace fits desktop')
  await open('Open Fact f001')
  await inspector.getByRole('button', { name: 'Show in Log', exact: true }).click()
  await page.getByRole('region', { name: 'Revision 2', exact: true }).waitFor()
  for (const viewport of [{ width: 320, height: 568 }, { width: 768, height: 1024 }]) {
    await page.setViewportSize(viewport)
    const toggle = page.getByRole('button', { name: 'Toggle right inspection panel', exact: true, includeHidden: true })
    await toggle.waitFor({ state: 'attached' })
    const drawer = page.getByRole('dialog', { name: 'Project inspection', exact: true })
    if (!await drawer.isVisible()) await toggle.click()
    await drawer.getByRole('tab', { name: 'Details', exact: true }).click()
    assert(await drawer.evaluate(el => el.scrollWidth <= el.clientWidth), 'Drawer fits narrow screen')
    assert(await drawer.getByRole('region', { name: 'Record Detail', exact: true }).isVisible(), 'Narrow screen retains details')
    await page.screenshot({ path: `dist/frontend-catalog/workspace-${viewport.width}.png`, animations: 'disabled' })
    await page.keyboard.press('Escape')
    await drawer.waitFor({ state: 'hidden' })
    assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Narrow graph fits page')
  }
  await page.setViewportSize({ width: 1440, height: 900 })
  await toggle.click()
  await page.getByRole('tab', { name: 'Details', exact: true }).click()
  await close()
  await page.evaluate(() => window.scrollTo(0, 0))
  return { workspaceRead: 'passed', facts: detail.facts.length, intents: detail.intents.length, revision, actualRun: execution.selected_intent_run.run_id }
}
