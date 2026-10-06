import { expect, test } from '@playwright/test'
import type { Page } from '@playwright/test'

const user = {
  id: 'test-user',
  client_id: 'test-tenant',
  name: 'Ada Lovelace',
  email: 'ada@example.test',
  created_at: new Date().toISOString(),
}
const login = {
  access_token: 'test-account-session',
  user,
  expires_at: new Date(Date.now() + 86400_000).toISOString(),
}
const key = {
  id: '12345678901234567890123456789012',
  name: 'Production scraper',
  created_at: new Date().toISOString(),
  expires_at: new Date(Date.now() + 30 * 86400_000).toISOString(),
}
const secret = 'nb_key_test.synthetic-test-secret'

async function mockAPI(page: Page) {
  let created = false
  let revoked = false
  await page.route('**/v1/**', async (route) => {
    const { pathname } = new URL(route.request().url())
    const method = route.request().method()
    if (pathname === '/v1/auth/login') {
      return route.fulfill({ json: login })
    }
    if (pathname === '/v1/auth/register')
      return route.fulfill({ status: 201, json: { user } })
    if (pathname === '/v1/auth/me') return route.fulfill({ json: { user } })
    if (pathname === '/v1/auth/logout') return route.fulfill({ status: 204 })
    if (pathname === '/v1/api-keys' && method === 'POST') {
      created = true
      return route.fulfill({ status: 201, json: { key, api_key: secret } })
    }
    if (pathname === '/v1/api-keys' && method === 'GET') {
      return route.fulfill({
        json: {
          keys: created
            ? [
                {
                  ...key,
                  revoked_at: revoked ? new Date().toISOString() : undefined,
                },
              ]
            : [],
        },
      })
    }
    if (pathname.startsWith('/v1/api-keys/') && method === 'DELETE') {
      revoked = true
      return route.fulfill({ status: 204 })
    }
    return route.fulfill({
      status: 404,
      json: { error: { message: 'Unexpected test request' } },
    })
  })
}

async function signIn(page: Page) {
  await page.goto('/login')
  await page.getByLabel('Email address').fill(user.email)
  await page.getByLabel('Password', { exact: true }).fill('test-password-123')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(
    page.getByRole('heading', { name: 'API keys', exact: true }),
  ).toBeVisible()
}

test('protected routes, one-time secret, reload, revocation, and logout', async ({
  page,
}, info) => {
  await mockAPI(page)
  await page.goto('/api-keys')
  await expect(page).toHaveURL(/\/login$/)
  await page.screenshot({ path: info.outputPath('login.png'), fullPage: true })
  await signIn(page)
  await expect(page.getByText('Make your first connection.')).toBeVisible()
  await page
    .getByRole('button', { name: 'Create API key', exact: true })
    .click()
  await page.getByLabel('Key name').fill(key.name)
  await page.getByLabel('Expires after').selectOption('7')
  const issuedRequest = page.waitForRequest(
    (request) =>
      request.url().endsWith('/v1/api-keys') && request.method() === 'POST',
  )
  await page.getByRole('button', { name: 'Create key', exact: true }).click()
  expect((await issuedRequest).postDataJSON()).toEqual({
    name: key.name,
    ttl_seconds: 604800,
  })
  await expect(page.getByLabel('Generated API key')).toHaveValue(secret)
  const storageHasSecret = await page.evaluate(
    (value) =>
      JSON.stringify(sessionStorage).includes(value) ||
      JSON.stringify(localStorage).includes(value),
    secret,
  )
  expect(storageHasSecret).toBe(false)
  await page.getByRole('button', { name: 'I’ve saved my key' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await page.reload()
  await expect(page.getByRole('cell', { name: key.name })).toBeVisible()
  await expect(page.getByLabel('Generated API key')).toHaveCount(0)
  await page.screenshot({
    path: info.outputPath('api-keys.png'),
    fullPage: true,
  })
  await page.getByRole('button', { name: 'Revoke', exact: true }).click()
  await page.getByRole('button', { name: 'Keep key' }).click()
  await expect(page.getByText('Active', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Revoke', exact: true }).click()
  await page.getByRole('button', { name: 'Revoke key', exact: true }).click()
  await expect(page.getByText('Revoked', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(
    await page.evaluate(() =>
      sessionStorage.getItem('novelbot.account-session'),
    ),
  ).toBeNull()
})

test('registration validates passwords and opens the workspace', async ({
  page,
}) => {
  await mockAPI(page)
  await page.goto('/register')
  await page.getByLabel('Full name').fill(user.name)
  await page.getByLabel('Email address').fill(user.email)
  await page.getByLabel('Password', { exact: true }).fill('short')
  await page
    .getByRole('button', { name: 'Create account', exact: true })
    .click()
  await expect(page.getByRole('alert')).toContainText('between 12 and 72 bytes')
  await page.getByLabel('Password', { exact: true }).fill('test-password-123')
  await page
    .getByRole('button', { name: 'Create account', exact: true })
    .click()
  await expect(page).toHaveURL(/\/api-keys$/)
})

test('login errors and rate limits are visible and retryable', async ({
  page,
}) => {
  await mockAPI(page)
  await page.route('**/v1/auth/login', (route) =>
    route.fulfill({
      status: 401,
      json: { error: { message: 'Invalid email or password' } },
    }),
  )
  await page.goto('/login')
  await page.getByLabel('Email address').fill(user.email)
  await page.getByLabel('Password', { exact: true }).fill('incorrect-password')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText(
    'Invalid email or password',
  )
  await page.route('**/v1/auth/login', (route) =>
    route.fulfill({
      status: 429,
      headers: { 'Retry-After': '25' },
      json: { error: { message: 'Too many requests' } },
    }),
  )
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('Try again in 25 seconds')
  await expect(
    page.getByRole('button', { name: 'Sign in', exact: true }),
  ).toBeEnabled()
})

test('expired server session clears credentials and returns to login', async ({
  page,
}) => {
  await mockAPI(page)
  await signIn(page)
  await page.route('**/v1/auth/me', (route) =>
    route.fulfill({
      status: 401,
      json: { error: { message: 'A valid login session is required' } },
    }),
  )
  await page.reload()
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.getByRole('status')).toContainText('Your session expired')
  expect(
    await page.evaluate(() =>
      sessionStorage.getItem('novelbot.account-session'),
    ),
  ).toBeNull()
})

test('mobile console and key dialog fit the viewport', async ({
  page,
}, info) => {
  await mockAPI(page)
  await page.setViewportSize({ width: 390, height: 844 })
  await signIn(page)
  await expect(page.getByText('Make your first connection.')).toBeVisible()
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true)
  await page.screenshot({ path: info.outputPath('mobile.png'), fullPage: true })
  await page
    .getByRole('button', { name: 'Create API key', exact: true })
    .click()
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(
    await page
      .getByRole('dialog')
      .evaluate(
        (element) => element.getBoundingClientRect().right <= window.innerWidth,
      ),
  ).toBe(true)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('real API: register, issue a working key, revoke it, and sign in again', async ({
  page,
  request,
}) => {
  test.skip(
    !process.env.UI_TEST_API_TARGET,
    'Set UI_TEST_API_TARGET to an isolated API to run this integration check.',
  )
  const email = `ui-${crypto.randomUUID()}@example.test`
  await page.goto('/register')
  await page.getByLabel('Full name').fill('UI Integration Test')
  await page.getByLabel('Email address').fill(email)
  await page
    .getByLabel('Password', { exact: true })
    .fill('isolated-test-password-123')
  await page
    .getByRole('button', { name: 'Create account', exact: true })
    .click()
  await expect(page).toHaveURL(/\/api-keys$/)
  await page
    .getByRole('button', { name: 'Create API key', exact: true })
    .click()
  await page.getByLabel('Key name').fill('Integration key')
  await page.getByRole('button', { name: 'Create key', exact: true }).click()
  const value = await page.getByLabel('Generated API key').inputValue()
  // Assert only status codes: never write issued credentials to test artifacts.
  expect(
    (
      await request.get('/v1/whoami', {
        headers: { Authorization: `Bearer ${value}` },
      })
    ).status(),
  ).toBe(200)
  await page.getByRole('button', { name: 'I’ve saved my key' }).click()
  await page.reload()
  await expect(
    page.getByRole('cell', { name: 'Integration key' }),
  ).toBeVisible()
  await page.getByRole('button', { name: 'Revoke', exact: true }).click()
  await page.getByRole('button', { name: 'Revoke key' }).click()
  await expect(page.getByText('Revoked', { exact: true })).toBeVisible()
  expect(
    (
      await request.get('/v1/whoami', {
        headers: { Authorization: `Bearer ${value}` },
      })
    ).status(),
  ).toBe(401)
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  await page.getByLabel('Email address').fill(email)
  await page
    .getByLabel('Password', { exact: true })
    .fill('isolated-test-password-123')
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await expect(page).toHaveURL(/\/api-keys$/)
})
