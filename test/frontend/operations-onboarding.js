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
  const origin = new URL(page.url()).origin
  const assert = (condition, message) => { if (!condition) throw new Error(message) }
  const button = name => page.getByRole('button', { name, exact: true })
  const input = name => page.getByLabel(name, { exact: true })
  const read = async path => {
    const response = await page.request.get(`${origin}/api/v1${path}`)
    assert(response.ok(), `Read ${path}: HTTP ${response.status()}`)
    return response.json()
  }
  const saveSettings = async scope => {
    await button('Save').click()
    await page.getByText(`${scope} saved.`, { exact: true }).waitFor()
  }
  const live = () => page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.setViewportSize({ width: 1440, height: 900 })
  assert((await read('/configuration')).draft.workers.length === 0, 'Onboarding begins without configured Workers')
  await page.getByRole('link', { name: 'Open Workers', exact: true }).click()
  await page.getByRole('link', { name: 'Runtime', exact: true }).click()
  await page.getByRole('tab', { name: 'Capacity', exact: true }).click()
  const recovery = name => page.getByRole('alert', { name: `${name} recovery`, exact: true })
  const beforeInvalid = await read('/configuration')
  await input('Global Active Run limit').fill('')
  await button('Save').click()
  await page.getByRole('alert').filter({ hasText: 'Settings could not be saved' }).waitFor()
  assert((await read('/configuration')).draft_version === beforeInvalid.draft_version, 'Incomplete settings do not change effective configuration')
  await input('Global Active Run limit').fill(String(beforeInvalid.draft.runtime.max_workers))
  await page.getByRole('link', { name: 'Runtime', exact: true }).click()
  await recovery('Workers').getByRole('link', { name: /^Open section\s*:\s*Workers$/ }).click()
  await page.getByRole('heading', { name: 'Codex Workers', exact: true, level: 2 }).waitFor()
  await page.getByRole('link', { name: 'New Worker', exact: true }).click()
  await input('Worker name').fill('onboarding-worker')
  for (const task of ['Bootstrap', 'Reason', 'Explore']) await page.getByRole('checkbox', { name: task, exact: true }).check()
  await input('Capacity').fill('1')
  await button('Continue to Provider').click()
  await selectOption(input('Provider configuration'), 'DeepSeek + Codex')
  await input('Responses Base URL').fill('http://127.0.0.1:PROVIDER_PORT/worker-check')
  await input('Model').fill('onboarding-model')
  await input('OpenAI API Key').fill('isolated-onboarding-secret')
  await button('Continue to Review').click()
  await button('Save Worker').click()
  await page.getByText('Worker saved.', { exact: true }).waitFor()
  await page.getByRole('link', { name: 'Review saved settings', exact: true }).click()
  await input('Scheduler interval (seconds)').waitFor()
  assert(new URL(page.url()).pathname === '/settings/runtime-capacity', 'Worker Save offers effective settings inspection')
  assert(await button('Apply').count() === 0, 'Worker Save needs no Apply')
  for (const section of ['Runtime & Capacity', 'Task Timeouts', 'Retry Policies', 'Project Runtime', 'Worker Healthchecks', 'Prompt Group']) {
    const destinations = {
      'Task Timeouts': ['Tasks', 'Task Timeouts'],
      'Retry Policies': ['Tasks', 'Retry Policies'],
      'Project Runtime': ['Runtime', 'Project'],
      'Runtime & Capacity': ['Runtime', 'Capacity'],
      'Worker Healthchecks': ['Runtime', 'Healthchecks'],
      'Prompt Group': ['Prompts', null],
    }
    const [category, tab] = destinations[section]
    await page.getByRole('link', { name: category, exact: true }).click()
    if (tab) await page.getByRole('tab', { name: tab, exact: true }).click()
    await button('Use recommended values').click()
    if (section === 'Project Runtime') {
      await input('Worker runtime image').fill('WORKER_IMAGE')
      await input('Network mode').fill('host')
      await selectOption(input('Completed action'), 'Stop')
    }
    if (await button('Save').isEnabled()) await saveSettings(section)
  }
  await page.getByText('Project creation ready', { exact: true }).waitFor()
  const active = await read('/configuration/revisions/active')
  assert(active.configuration.workers[0].name === 'onboarding-worker', 'Saved effective revision includes browser-saved Worker')
  assert(!JSON.stringify(active).includes('isolated-onboarding-secret'), 'Applied configuration masks saved credentials')
  await page.getByRole('link', { name: 'Projects', exact: true }).last().click()
  await button('New Project').click()
  const creation = page.getByRole('dialog', { name: 'Create Project', exact: true })
  const title = 'Integrated Operations demonstration'
  const description = 'Known evidence · 已核实事实 · دليل موثوق · 🚀\n'.repeat(30).trim()
  await creation.getByLabel('Title', { exact: true }).fill(title)
  await creation.getByLabel('Origin', { exact: true }).fill(description)
  await creation.getByLabel('Goal', { exact: true }).fill('Verify the integrated operator workflow')
  await creation.getByRole('checkbox', { name: 'Bootstrap', exact: true }).uncheck()
  const receipt = page.waitForResponse(response => response.url().endsWith('/api/v1/projects') && response.request().method() === 'POST')
  await creation.getByRole('button', { name: 'Create Project', exact: true }).click()
  const created = await (await receipt).json()
  const path = `/projects/${created.project.id}`
  await page.waitForURL(`${origin}${path}`)
  await live()
  assert(created.project.config_revision === active.id, 'Catalog creation binds applied Settings and enters Workspace')
  await button('Add Hint').click()
  const hintDialog = page.getByRole('dialog', { name: 'Add Hint', exact: true })
  await hintDialog.getByLabel('Hint text', { exact: true }).fill('Use the verified Origin as completion evidence')
  await hintDialog.getByRole('button', { name: 'Add Hint', exact: true }).click()
  await hintDialog.waitFor({ state: 'hidden' })
  await live()
  assert((await read(path)).hints[0].creator === 'catalog-operator', 'Hint attribution remains server-owned')
  await button('Open Fact origin').focus()
  await button('Open Fact origin').click()
  const detail = page.getByRole('region', { name: 'Record Detail', exact: true })
  assert(await detail.getByText(description, { exact: true }).textContent() === description, 'Record inspection preserves complete multilingual evidence')
  await page.setViewportSize({ width: 390, height: 844 })
  const sheet = page.getByRole('dialog', { name: 'Project inspection', exact: true })
  assert(await sheet.evaluate(element => { const style = getComputedStyle(element); return style.animationName === 'none' && style.transitionDuration.split(',').every(duration => parseFloat(duration) === 0) }), 'Reduced motion disables Sheet movement')
  await page.keyboard.press('Escape')
  await sheet.waitFor({ state: 'hidden' })
  await page.setViewportSize({ width: 1440, height: 900 })
  const evidence = page.getByRole('checkbox', { name: 'Select origin as Completion Evidence', exact: true })
  await evidence.focus()
  await page.keyboard.press('Space')
  await page.getByText('1 Facts selected as Completion Evidence', { exact: true }).waitFor()
  await projectAction('Replay')
  const controls = page.getByRole('region', { name: 'Replay controls', exact: true })
  await controls.getByRole('button', { name: 'Pause Replay', exact: true }).waitFor()
  assert(await page.getByRole('checkbox', { checked: true }).count() === 0, 'Completion Evidence is cleared')
  await controls.getByRole('button', { name: 'Exit Replay', exact: true }).click()
  await live()
  await evidence.check()
  await button('Complete Project').click()
  const completion = page.getByRole('dialog', { name: 'Complete Project', exact: true })
  await completion.getByLabel('Completion description', { exact: true }).fill('Operator reviewed the selected Origin evidence')
  await completion.getByRole('button', { name: 'Complete Project', exact: true }).click()
  await completion.waitFor({ state: 'hidden' })
  await live()
  const completed = await read(path)
  assert(completed.project.status === 'completed' && completed.intents.some(intent => intent.kind === 'completion' && JSON.stringify(intent.from) === '["origin"]'), 'Selected evidence produces the authoritative Completion Intent')
  await projectAction('Replay')
  await controls.getByRole('button', { name: 'Pause Replay', exact: true }).waitFor()
  await selectOption(controls.getByLabel('Go to Replay frame', { exact: true }), 'R1')
  await controls.getByRole('status').filter({ hasText: 'Frame 1 of' }).waitFor()
  for (const name of ['Add Hint', 'Complete Project']) assert(await button(name).isDisabled(), `${name} is read-only in the historical Active frame`)
  assert(await button('Pause').isDisabled(), 'Historical Active frame disables Pause')
  await button('Project actions').click()
  for (const name of ['Rename', 'Delete Project']) assert(await page.getByRole('menuitem', { name, exact: true }).getAttribute('aria-disabled') === 'true', `${name} is read-only in the historical Active frame`)
  await page.keyboard.press('Escape')
  assert(await evidence.isDisabled(), 'Historical Facts cannot become Completion Evidence')
  await selectOption(controls.getByLabel('Go to Replay frame', { exact: true }), `R${completed.project.revision}`)
  await controls.getByRole('status').filter({ hasText: `Frame ${completed.project.revision} of` }).waitFor()
  assert(await button('Reopen').isDisabled(), 'Historical Completed frame also disables Reopen')
  await controls.getByRole('button', { name: 'Exit Replay', exact: true }).click()
  await live()
  await page.reload()
  await live()
  await page.getByRole('heading', { name: title, exact: true }).waitFor()
  assert(await button('Reopen').isEnabled(), 'Actual Project deep-link refresh recovers live command eligibility')
  await projectAction('Delete')
  const deletion = page.getByRole('alertdialog', { name: 'Delete Project', exact: true })
  await deletion.getByLabel('Project title confirmation', { exact: true }).fill(title)
  await deletion.getByRole('button', { name: 'Delete Project', exact: true }).click()
  await page.waitForURL(`${origin}/projects`)
  await page.getByText('No Projects yet', { exact: true }).waitFor()
  assert((await page.request.get(`${origin}/api/v1${path}`)).status() === 404, 'Demonstration deletes only its isolated Project')
  return { integratedOnboarding: 'passed', selectedEvidenceCompletion: 'passed', historicalReadOnlyAndLiveRecovery: 'passed', reducedMotion: 'passed' }
}
