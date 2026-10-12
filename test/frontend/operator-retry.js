async page => {
  const assert = (value, message) => { if (!value) throw new Error(message) }
  const origin = new URL(page.url()).origin
  assert(new URL(origin).port !== '8000', 'Retry acceptance requires an isolated backend')
  const id = new URL(page.url()).pathname.split('/').at(-1)
  const path = `/api/v1/projects/${id}`
  const before = await (await page.request.get(`${origin}${path}`)).json()
  await page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Project actions', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Retry exhausted work', exact: true }).click()
  const dialog = page.getByRole('alertdialog', { name: 'Retry exhausted work', exact: true })
  await dialog.waitFor()
  const response = page.waitForResponse(r => r.url().endsWith('/actions/retry-exhausted') && r.request().method() === 'POST')
  await dialog.getByRole('button', { name: 'Retry exhausted work', exact: true }).click()
  const result = await response
  assert(result.ok(), `Retry failed: ${result.status()} ${await result.text()}`)
  const receipt = await result.json()
  assert(receipt.run_ids.length === 1 && receipt.replayed === false, 'Exactly the exhausted task gets a new budget')
  await dialog.waitFor({ state: 'hidden' })
  await page.getByText('Retry requested for 1 task', { exact: true }).waitFor()
  const run = await (await page.request.get(`${origin}${path}/activity/${receipt.run_ids[0]}`)).json()
  assert(run.status === 'failed' && run.operator_retries.length === 1, 'Original failure and Operator audit remain readable')
  assert(run.operator_retries[0].operator_name === 'retry-browser-operator' && run.operator_retries[0].previous_failure_count === 1, 'Audit uses server-owned Operator identity and previous budget')
  const after = await (await page.request.get(`${origin}${path}`)).json()
  assert(after.project.revision === before.project.revision && after.facts.length === before.facts.length, 'Retry preserves Blackboard')
  await page.screenshot({ path: '/tmp/aether-operator-retry-desktop.png', animations: 'disabled' })
  await page.setViewportSize({ width: 320, height: 720 })
  await page.getByLabel('Data Freshness: Live', { exact: true }).waitFor()
  await page.getByRole('button', { name: 'Project actions', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Retry exhausted work', exact: true }).click()
  await dialog.waitFor()
  assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), 'Retry dialog fits a phone viewport')
  await page.screenshot({ path: '/tmp/aether-operator-retry-mobile.png', animations: 'disabled' })
  await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  await dialog.waitFor({ state: 'hidden' })
  assert(await page.getByRole('button', { name: 'Project actions', exact: true }).evaluate(el => el === document.activeElement), 'Cancel restores menu trigger focus')
  return { operatorRetry: 'passed', preservedEvidence: 'passed', unchangedBlackboard: 'passed', mobileAndFocus: 'passed' }
}
