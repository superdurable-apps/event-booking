import { expect, test } from '@playwright/test';

test('serves the closed-by-default production event page', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { level: 1, name: 'Event details coming soon' })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Registration is not open' })).toBeDisabled();
  await expect(page.getByText('300 of 300 remaining')).toBeVisible();
});
