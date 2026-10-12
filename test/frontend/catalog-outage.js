async page => {
  await page.getByRole('button', { name: 'Refresh', exact: true }).click()
  await page.getByText('Catalog update unavailable', { exact: true }).waitFor()
  await page.waitForFunction(() => [...document.querySelectorAll('button')].find(element => element.textContent === 'New Project')?.disabled)
  if (!await page.getByRole('table', { name: 'Projects', exact: true }).isVisible()) throw new Error('Last loaded Catalog must remain available during outage')
  if (!await page.getByLabel('Total Projects: 9', { exact: true }).isVisible()) throw new Error('Last loaded statistics must remain during outage')
  return { realServiceOutage: 'passed', retainedCatalog: true }
}
