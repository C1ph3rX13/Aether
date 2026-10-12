async page => {
  const table = page.getByRole('table', { name: 'Projects', exact: true })
  const cells = table.getByRole('row').locator('td:nth-child(6)')
  const before = await cells.allTextContents()
  const response = await page.request.get(new URL('/api/v1/projects', page.url()).href)
  if (response.status() !== 500 || (await response.json()).code !== 'internal_error') throw new Error('Corrupt Reason evidence must fail the complete real HTTP Catalog')
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await page.getByText('Catalog update unavailable', { exact: true }).waitFor()
  if (before.length !== 7 || JSON.stringify(await cells.allTextContents()) !== JSON.stringify(before)) throw new Error('Corrupt evidence must retain the complete last valid Catalog without synthesizing Idle')
  await page.reload()
  await page.getByText('Project Catalog unavailable', { exact: true }).waitFor()
  if (await table.isVisible()) throw new Error('Initial corrupt read must not show a partial Catalog')
  return { corruptReasonRead: 'passed', retainedValidEvidence: 'passed', initialFailClosed: 'passed' }
}
