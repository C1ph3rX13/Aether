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
    const value = structuredClone(draft)
    for (const worker of value.workers) delete worker.secrets
    return value
  }
  const input = name => page.getByLabel(name, { exact: true })
  const button = name => page.getByRole('button', { name, exact: true })
  const worker = (read, name) => read.draft.workers.find(entry => entry.name === name)
  const waitSave = () => page.waitForResponse(response => response.url().endsWith('/api/v1/configuration') && response.request().method() === 'PUT')
  const save = async (status = 200) => {
    const pending = waitSave()
    await button('Save Worker').click()
    const response = await pending
    assert(response.status() === status, `Worker save expected ${status}, got ${response.status()}`)
    if (status === 200) await page.getByText('Worker saved.', { exact: true }).waitFor()
    return { read: await response.json(), request: response.request().postDataJSON() }
  }
  const edit = async (branch, name) => {
    await page.goto(`${origin}/workers/${branch}/editor?worker=${encodeURIComponent(name)}`)
    await input('Worker name').waitFor()
  }
  await page.setViewportSize({ width: 1440, height: 900 })
  const initial = await api('/configuration')
  const prior = await api('/configuration/revisions/active')
  const branches = [
    { path: 'codex', type: 'codex', label: 'Codex', preset: 'deepseek_codex', url: 'Responses Base URL', secret: 'OpenAI API Key', slot: 'OPENAI_API_KEY' },
    { path: 'claude', type: 'claude_code', label: 'Claude', preset: 'deepseek_claude_code', url: 'Anthropic Base URL', secret: 'Anthropic Auth Token', slot: 'ANTHROPIC_AUTH_TOKEN' },
    { path: 'pi', type: 'pi', label: 'Pi', preset: 'deepseek_pi', url: 'Provider Base URL', secret: 'Provider API Key', slot: 'PI_API_KEY' },
  ]
  let saved = initial
  for (const branch of branches) {
    await page.goto(`${origin}/workers/${branch.path}`)
    await page.getByRole('link', { name: 'New Worker', exact: true }).click()
    await input('Worker name').fill(`${branch.path}-created`)
    assert(await input('Agent Type').inputValue() === saved.agent_definitions.find(definition => definition.type === branch.type).display_name, 'Catalog fixes the Agent Definition')
    for (const task of ['Bootstrap', 'Reason', 'Explore']) assert(await page.getByRole('checkbox', { name: task, exact: true }).isChecked(), 'New Workers enable every task by default')
    await input('Capacity').fill('3')
    await input('Priority').fill('2')
    await page.locator('[aria-label="Worker summary"] > p').scrollIntoViewIfNeeded()
    assert(await page.evaluate(() => {
      const note = document.querySelector('[aria-label="Worker summary"] > p').getBoundingClientRect()
      const footer = document.getElementById('worker-actions').getBoundingClientRect()
      return note.bottom <= footer.top && note.top >= 0
    }), `${branch.path}: complete Worker summary stays visible above sticky controls on desktop`)
    if (branch.path === 'codex') await page.screenshot({ path: 'dist/frontend-catalog/worker-step-basic-1440.png', animations: 'disabled' })
    await button('Continue to Provider').click()
    await selectOption(input('Provider configuration'), saved.provider_presets.find(preset => preset.id === branch.preset).display_name)
    const preset = saved.provider_presets.find(preset => preset.id === branch.preset)
    assert(await input('Model').inputValue() === preset.agent_env[branch.type === 'codex' ? 'CODEX_MODEL' : branch.type === 'pi' ? 'PI_MODEL' : 'ANTHROPIC_MODEL'], 'Ordinary recommended fields come from real Provider metadata')
    assert(await input('Model').inputValue() === 'deepseek-flash', 'Every DeepSeek Worker creation preset uses deepseek-flash')
    if (branch.path === 'pi') {
      const protocol = page.getByRole('combobox', { name: 'Provider API', exact: true })
      await protocol.click()
      const listbox = page.getByRole('listbox')
      await listbox.waitFor()
      const options = listbox.getByRole('option')
      assert(await options.count() === 3, 'Pi exposes all three supported protocols')
      for (const label of ['Anthropic Messages', 'OpenAI Responses', 'OpenAI Chat Completions']) assert(await listbox.getByRole('option', { name: label, exact: true }).count() === 1, 'Protocol labels come from Agent Definition metadata')
      await page.screenshot({ path: 'dist/frontend-catalog/pi-provider-api.png', animations: 'disabled' })
      await listbox.getByRole('option', { name: 'OpenAI Chat Completions', exact: true }).click()
    }
    await input(branch.url).fill('http://127.0.0.1:PROVIDER_PORT/worker-check')
    await input('Model').fill('controlled-worker-model')
    await input(branch.secret).fill(`credential-${branch.path}-created`)
    if (branch.path === 'codex') await page.screenshot({ path: 'dist/frontend-catalog/worker-step-provider-1440.png', animations: 'disabled' })
    await button('Continue to Review').click()
    await page.getByText('Ready for the Provider check', { exact: true }).waitFor()
    if (branch.path === 'codex') await page.screenshot({ path: 'dist/frontend-catalog/worker-step-review-1440.png', animations: 'disabled' })
    await button('Edit provider configuration').click()
    assert(await input(branch.secret).inputValue() === `credential-${branch.path}-created`, 'Steps preserve sensitive replacement input')
    await button('Continue to Review').click()
    const result = await save()
    same(result.request.draft.runtime, initial.draft.runtime, 'Worker save preserves captured System settings')
    same(result.request.draft.tasks, initial.draft.tasks, 'Worker save preserves captured Project settings')
    same(result.request.draft.container, { ...initial.draft.container, cap_add: initial.draft.container.cap_add ?? [] }, 'Worker save preserves Project Runtime')
    same(result.read.draft.workers.filter(entry => entry.name !== `${branch.path}-created`), saved.draft.workers, 'Worker save preserves unrelated Workers and Secret state')
    assert(result.request.expected_draft_version === saved.draft_version, 'Editor owns a captured complete Draft/version')
    assert(worker(result.read, `${branch.path}-created`).secrets[branch.slot].configured, 'Real candidate credential check and atomic save succeeded')
    assert(!JSON.stringify(result.read).includes(`credential-${branch.path}-created`), 'Saved configuration never returns plaintext')
    await page.waitForURL(`**/workers/${branch.path}/editor?worker=${branch.path}-created`)
    await input('Worker name').waitFor()
    await page.getByText('Worker saved.', { exact: true }).waitFor()
    assert(await page.getByRole('link', { name: branch.label, exact: true }).getAttribute('aria-current') === 'page', 'Editor retains active Worker shell branch')
    assert(await input('Worker name').inputValue() === `${branch.path}-created`, 'Saved route reloads authoritative identity')
    assert(await input('Replace ' + branch.secret).count() === 0, 'Successful save removes credential inputs')
    await input('Priority').fill('9')
    await input('Priority').fill('2')
    assert(!await page.getByText('Worker saved.', { exact: true }).isVisible(), 'Reverting edits does not resurrect a previous save notice')
    await page.reload()
    await input('Worker name').waitFor()
    assert((await api('/configuration/revisions/active')).id === result.read.active_revision.id, 'Save immediately publishes the effective revision')
    saved = result.read
    await page.getByRole('link', { name: 'Back to Workers', exact: true }).click()
    await page.getByRole('button', { name: `View ${branch.path}-created configuration`, exact: true }).click()
    await page.getByRole('link', { name: `Edit ${branch.path}-created`, exact: true }).click()
    await input('Worker name').waitFor()
    assert(await input('Agent Type').inputValue() === saved.agent_definitions.find(definition => definition.type === branch.type).display_name, 'Every catalog opens its fixed Edit branch')
    await page.getByRole('link', { name: 'Back to Workers', exact: true }).click()
    await page.getByRole('button', { name: `View ${branch.path}-created configuration`, exact: true }).click()
    await page.getByRole('link', { name: `Copy ${branch.path}-created`, exact: true }).click()
    await input('Worker name').waitFor()
    assert(await input('Worker name').inputValue() === `${branch.path}-created-copy`, 'Every catalog opens its own Copy branch')
    await button('Continue to Provider').click()
    assert(await input(branch.secret).inputValue() === '', 'Every copied Agent excludes its source credentials')
    await page.getByRole('link', { name: 'Back to Workers', exact: true }).click()
    await page.getByRole('dialog', { name: 'Unsaved settings', exact: true }).getByRole('button', { name: 'Discard', exact: true }).click()
    await page.getByRole('heading', { name: `${branch.path}-created`, exact: true }).waitFor()
  }

  // Browser edits use retained credentials. Rejected candidate replacements leave
  // both Draft and Secret ownership unchanged, with bounded masked diagnostics.
  await edit('codex', 'codex-created')
  await input('Priority').fill('4')
  const edited = await save()
  assert(!('secrets' in edited.request), 'An ordinary edit retains the configured Secret')
  await input('Worker name').waitFor()
  await button('Replace Secret').click()
  await input('Replace OpenAI API Key').fill('rejected-worker-credential')
  const failed = await save(503)
  assert(failed.read.code === 'provider_check_failed', 'Real controlled Provider rejects candidate credentials')
  assert(!JSON.stringify(failed.read).includes('rejected-worker-credential'), 'Go API redacts the echoed credential')
  assert(failed.read.detail.length < 350, 'Provider diagnostic is bounded')
  await page.getByRole('dialog', { name: 'Provider check failed', exact: true }).waitFor()
  await button('Keep editing').click()
  assert(await input('Replace OpenAI API Key').inputValue() === 'rejected-worker-credential', 'Failed save retains necessary replacement input')
  same(await api('/configuration'), edited.read, 'Provider rejection does not change Draft/version or Secret state')
  assert(!await page.getByText('Worker saved.', { exact: true }).isVisible(), 'Failure does not announce success')
  await input('Replace OpenAI API Key').fill('credential-codex-replaced')
  for (const [endpoint, status, code, title] of [
    ['http://127.0.0.1:PROVIDER_PORT/worker-check/rate-limit', 503, 'provider_check_failed', 'Provider check failed'],
    ['http://127.0.0.1:1', 503, 'provider_check_failed', 'Provider check failed'],
    ['not-a-url', 400, 'validation_failed', 'Worker could not be saved'],
  ]) {
    await input('Responses Base URL').fill(endpoint)
    const rejection = await save(status)
    assert(rejection.read.code === code, 'Rate-limit, network and structural failures use the real API error contract')
    await page.getByRole('dialog', { name: title, exact: true }).waitFor()
    await button('Keep editing').click()
    assert(await input('Responses Base URL').inputValue() === endpoint && await input('Replace OpenAI API Key').inputValue() === 'credential-codex-replaced', 'Failures preserve correctable ordinary and sensitive inputs')
    same(await api('/configuration'), edited.read, 'Failures leave the Draft and Secret state atomic')
  }
  await input('Responses Base URL').fill('http://127.0.0.1:PROVIDER_PORT/worker-check')
  await input('Replace OpenAI API Key').fill('credential-codex-replaced')
  const replaced = await save()
  await button('Replace Secret').click()
  assert(await input('Replace OpenAI API Key').inputValue() === '', 'Successful replacement erases sensitive input')
  await button('Keep current Secret').click()

  // Copy requires its own credential and cannot inherit source credentials.
  await page.getByRole('link', { name: 'Back to Workers', exact: true }).click()
  await page.getByRole('button', { name: 'View codex-created configuration', exact: true }).click()
  await page.getByRole('link', { name: 'Copy codex-created', exact: true }).click()
  await input('Worker name').waitFor()
  assert(await input('Worker name').inputValue() === 'codex-created-copy', 'Copy proposes a unique identity')
  await button('Continue to Provider').click()
  assert(await input('OpenAI API Key').inputValue() === '', 'Copy excludes Secret values')
  await button('Continue to Review').click()
  assert(await input('OpenAI API Key').getAttribute('aria-invalid') === 'true', 'Copy cannot save without required credentials')
  await input('OpenAI API Key').fill('credential-copy-explicit')
  await button('Continue to Review').click()
  const completeCopy = await save()
  assert(worker(completeCopy.read, 'codex-created-copy').secrets.OPENAI_API_KEY.configured, 'Copy explicitly configures its own credential')
  await button('Clear Secret').click()
  await button('Save Worker').click()
  await page.getByText('Enter the required credential before saving.', { exact: true }).first().waitFor()
  same(await api('/configuration'), completeCopy.read, 'Clearing a required credential does not change saved configuration')
  await button('Keep current Secret').click()

  // A rename requires a new credential and makes the new identity effective on Save.
  await edit('codex', 'codex-created')
  await input('Worker name').fill('codex-renamed')
  await page.getByText('Name changed; credentials reset. Configure Secrets for the new name.', { exact: true }).waitFor()
  await page.getByText('Not configured', { exact: true }).waitFor()
  await button('Configure Secret').click()
  await input('Replace OpenAI API Key').fill('credential-renamed-explicit')
  const renamed = await save()
  assert(worker(renamed.read, 'codex-renamed').secrets.OPENAI_API_KEY.configured, 'Renamed identity stores its explicit credential')
  assert(!worker(renamed.read, 'codex-created'), 'Rename replaces the owned Worker')
  await page.reload()
  await input('Worker name').waitFor()
  assert(await input('Worker name').inputValue() === 'codex-renamed', 'Rename reload keeps the authoritative new identity')
  await page.getByText(/^Configured\s*\*{8}$/).waitFor()

  // Real CAS conflict preserves edits and credentials until an explicit Reload.
  await input('Priority').fill('8')
  await button('Replace Secret').click()
  await input('Replace OpenAI API Key').fill('credential-conflict-local')
  const otherDraft = clean(renamed.read.draft)
  otherDraft.runtime.scheduler_interval_seconds += 1
  const concurrent = await api('/configuration', { expected_draft_version: renamed.read.draft_version, draft: otherDraft }, 'put')
  await save(409)
  const conflict = page.getByRole('dialog', { name: 'Settings changed elsewhere', exact: true })
  await conflict.getByRole('button', { name: 'Keep editing', exact: true }).click()
  await conflict.waitFor({ state: 'hidden' })
  assert(await input('Priority').inputValue() === '8', 'Conflict keeps local fields')
  assert(await input('Replace OpenAI API Key').inputValue() === 'credential-conflict-local', 'Conflict keeps replacement input')
  assert(await button('Save Worker').isDisabled(), 'Conflict never retries a stale version')
  await button('Reload').click()
  await conflict.getByRole('button', { name: 'Reload', exact: true }).click()
  await page.waitForFunction(() => document.getElementById('worker-priority')?.value === '4')
  assert(await input('Replace OpenAI API Key').count() === 0, 'Successful explicit Reload clears credential input')
  assert((await api('/configuration')).draft_version === concurrent.draft_version, 'Reload does not write or merge')
  await input('Priority').fill('6')
  const recovered = await save()
  assert(recovered.request.expected_draft_version === concurrent.draft_version, 'Recovered save uses reloaded complete Draft/version')
  assert(recovered.read.draft.runtime.scheduler_interval_seconds === concurrent.draft.runtime.scheduler_interval_seconds, 'Recovered Worker save preserves concurrent System settings')

  // Dirty navigation retains its destination; Save commits before leaving.
  await input('Priority').fill('7')
  await page.getByRole('link', { name: 'Back to Workers', exact: true }).click()
  const navigation = page.getByRole('dialog', { name: 'Unsaved settings', exact: true })
  await navigation.getByRole('button', { name: 'Cancel', exact: true }).click()
  await navigation.waitFor({ state: 'hidden' })
  assert(await input('Priority').inputValue() === '7', 'Cancel preserves Worker edits')
  await page.getByRole('link', { name: 'Back to Workers', exact: true }).click()
  const leavingSave = waitSave()
  await navigation.getByRole('button', { name: 'Save', exact: true }).click()
  assert((await leavingSave).ok(), 'Save-and-leave uses the real Worker save')
  await page.getByRole('heading', { name: 'codex-renamed', exact: true }).waitFor()
  await page.getByRole('button', { name: 'View codex-renamed configuration', exact: true }).click()
  await page.getByRole('link', { name: 'Edit codex-renamed', exact: true }).click()
  await input('Priority').waitFor()
  assert(await input('Priority').inputValue() === '7', 'Catalog edit reloads saved Worker values')
  await input('Priority').fill('8')
  await page.getByRole('link', { name: 'Runtime', exact: true }).click()
  await navigation.getByRole('button', { name: 'Discard', exact: true }).click()
  await page.getByRole('tab', { name: 'Capacity', exact: true }).click()
  await input('Scheduler interval (seconds)').waitFor()
  assert(worker(await api('/configuration'), 'codex-renamed').priority === 7, 'Discard never submits the Worker changes')
  await edit('codex', 'codex-renamed')

  // Hold a genuine committed Go response to prove pending-write coordination.
  let committed
  const received = new Promise(resolve => { committed = resolve })
  let release
  const released = new Promise(resolve => { release = resolve })
  let writes = 0
  await page.route('**/api/v1/configuration', async route => {
    if (route.request().method() !== 'PUT') return route.continue()
    writes++
    const response = await route.fetch()
    committed()
    await released
    await route.fulfill({ response })
  })
  await input('Capacity').fill('4')
  const pendingSave = save()
  await received
  assert(await button('Checking Provider...').isDisabled(), 'Pending Save prevents duplicate writes')
  assert(await button('New Project').isDisabled(), 'Pending Worker Save and Provider check exclude global creation')
  await page.getByRole('link', { name: 'Runtime', exact: true }).click()
  assert(new URL(page.url()).pathname === '/workers/codex/editor', 'Pending write prevents abandoning its result')
  assert(writes === 1, 'No pending automatic resubmission')
  release()
  await pendingSave
  await page.unroute('**/api/v1/configuration')

  // Lose the browser response after a genuine committed write. The browser
  // cannot infer cancellation, claim success or automatically resend the PUT.
  const beforeInterrupted = await api('/configuration')
  let interruptedRead
  writes = 0
  await page.route('**/api/v1/configuration', async route => {
    if (route.request().method() !== 'PUT') return route.continue()
    writes++
    const response = await route.fetch()
    interruptedRead = await response.json()
    await route.abort('failed')
  })
  await button('Replace Secret').click()
  await input('Replace OpenAI API Key').fill('credential-uncertain-write')
  const transportFailure = page.waitForEvent('requestfailed', { predicate: request => request.url().endsWith('/api/v1/configuration') && request.method() === 'PUT' })
  await button('Save Worker').click()
  await transportFailure
  const interrupted = page.getByRole('dialog', { name: 'Worker could not be saved', exact: true })
  await interrupted.waitFor()
  assert((await interrupted.innerText()).includes('reload to check saved settings first'), 'Interrupted write explains explicit read recovery')
  await interrupted.getByRole('button', { name: 'Keep editing', exact: true }).click()
  await interrupted.waitFor({ state: 'hidden' })
  assert(await input('Replace OpenAI API Key').inputValue() === 'credential-uncertain-write', 'Transport failure retains correction input')
  assert(!await page.getByText('Worker saved.', { exact: true }).isVisible() && writes === 1, 'Uncertain result neither announces success nor automatically retries')
  const committedRead = await api('/configuration')
  assert(committedRead.draft_version === beforeInterrupted.draft_version + 1, 'Abandoning the browser response did not cancel the server write')
  same(committedRead, interruptedRead, 'Supported read resolves the authoritative committed state')
  await page.unroute('**/api/v1/configuration')
  assert((await api('/configuration/revisions/active')).id === committedRead.active_revision.id, 'Even interrupted receipts leave the committed revision effective')
  same(await api(`/configuration/revisions/${encodeURIComponent(prior.id)}`), prior, 'Historical revision remains immutable')
  return { threeAgentEditors: 'passed', realCandidateCheckAndAtomicFailure: 'passed', retainReplaceClearCopyRename: 'passed', capturedDraftConflictRecovery: 'passed', guardedNavigationAndPendingWrite: 'passed', interruptedWrite: 'passed' }
}
