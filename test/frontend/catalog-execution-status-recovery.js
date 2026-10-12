async page => {
  await page.getByText('Catalog update unavailable', { exact: true }).waitFor({ state: 'hidden' })
  const table = page.getByRole('table', { name: 'Projects', exact: true })
  if (await table.getByRole('row').count() !== 8) throw new Error('Recovered execution Catalog retains the search and all seven rows')
  if (await table.getByRole('row').locator('td:nth-child(4) [data-slot="badge"]').count() !== 7) throw new Error('Recovered execution cells each have one Badge')
  return { executionStatusRecovery: 'passed' }
}
