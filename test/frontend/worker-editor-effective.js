async page => {
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
  const input = name => page.getByLabel(name, { exact: true })
  const button = name => page.getByRole('button', { name, exact: true })
  const waitSave = () => page.waitForResponse(response => response.url().endsWith('/api/v1/configuration') && response.request().method() === 'PUT')
  const save = async (status = 200) => {
    const pending = waitSave()
    await button('Save Worker').click()
    const response = await pending
    assert(response.status() === status, `Worker save expected ${status}, got ${response.status()}`)
    if (status === 200) await page.getByText('Worker saved.', { exact: true }).waitFor()
    return { read: await response.json(), request: response.request().postDataJSON() }
  }
  const prior = await api('/configuration/revisions/active')
  await input('Worker name').waitFor()
  await page.getByText(/^Configured\s*\*{8}$/).waitFor()
  assert(await input('Replace OpenAI API Key').count() === 0, 'Explicit successful Reload clears sensitive input after an uncertain write')

  await page.setViewportSize({ width: 1024, height: 720 })
  await button('Timeout overrides').click()
  await input('Reason primary (seconds)').fill('37')
  await input('Reason primary (seconds)').focus()
  await page.keyboard.press('Tab')
  assert(await page.evaluate(() => {
    const target = document.activeElement.getBoundingClientRect()
    const footer = document.getElementById('worker-actions').getBoundingClientRect()
    return target.bottom <= footer.top && target.top >= 0 && document.documentElement.scrollWidth <= innerWidth
  }), 'Keyboard focus remains visible above sticky actions at minimum desktop')
  await page.screenshot({ path: 'dist/frontend-catalog/worker-editor-1024.png', animations: 'disabled' })
  await save()
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.screenshot({ path: 'dist/frontend-catalog/worker-editor-1440.png', animations: 'disabled' })

  // Worker Save is already effective; inspect it without another command.
  await page.getByRole('link', { name: 'Review saved settings', exact: true }).click()
  await input('Scheduler interval (seconds)').waitFor()
  assert(await button('Apply').count() === 0, 'Save needs no global Apply')
  await page.getByText('Project creation ready', { exact: true }).waitFor()
  const active = await api('/configuration/revisions/active')
  assert(active.id !== prior.id && active.configuration.workers.some(entry => entry.name === 'codex-renamed'), 'New applied revision contains saved Worker')
  assert(active.secret_references.some(reference => reference.worker_name === 'codex-renamed') && !active.secret_references.some(reference => reference.worker_name === 'codex-created'), 'Applied Secret ownership follows explicit renamed identity')
  same(await api(`/configuration/revisions/${encodeURIComponent(prior.id)}`), prior, 'Historical Worker configuration and Secret references remain intact')
  await button('Saved settings').click()
  const panel = page.getByRole('dialog', { name: 'Saved settings', exact: true })
  await panel.getByText('codex-renamed', { exact: true }).waitFor()
  assert(!(await panel.innerText()).includes('credential-'), 'Applied inspection masks stored credentials')
  await panel.getByRole('button', { name: 'Close', exact: true }).click()

  await page.goto(`${origin}/workers/pi/editor?worker=codex-renamed`)
  await page.getByRole('heading', { name: 'Worker not found', exact: true }).waitFor()
  assert(await input('Worker name').count() === 0, 'Another Agent branch cannot edit the Worker')
  return { interruptedWriteRecovery: 'passed', saveEffectiveAndHistoricalSemantics: 'passed', keyboardAndStickyControls: 'passed' }
}
