async page => {
  await page.getByText('Catalog update unavailable', { exact: true }).waitFor({ state: 'hidden' })
  await page.getByText('9 of 9 Projects', { exact: true }).waitFor()
  const configurationURL = new URL('/api/v1/configuration', page.url()).href
  const current = await (await page.request.get(configurationURL)).json()
  const draft = current.draft
  draft.workers[0].priority += 1
  for (const worker of draft.workers) delete worker.secrets
  const failedSave = await page.request.put(configurationURL, { data: { expected_draft_version: current.draft_version, draft } })
  if (failedSave.ok() || (await failedSave.json()).code !== 'provider_check_failed') throw new Error('Controlled Provider must actually fail its real health check')
  await page.reload()
  await page.getByText('Project creation ready', { exact: true }).waitFor()
  await page.getByText('9 of 9 Projects', { exact: true }).waitFor()
  return { realServiceRecovery: 'passed', providerIndependentCreation: 'passed' }
}
