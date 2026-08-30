import { useEffect, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, asList, errorText } from '../api/client'
import type { Customer, PriceBook } from '../api/types'
import { Field, Notice } from '../components/ui'

export function CustomerNew() {
  const nav = useNavigate()
  const [books, setBooks] = useState<PriceBook[]>([])
  const [slug, setSlug] = useState('')
  const [name, setName] = useState('')
  const [adminEmail, setAdminEmail] = useState('')
  const [priceBookId, setPriceBookId] = useState('')
  const [billingMode, setBillingMode] = useState('showback')
  const [startDate, setStartDate] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    api
      .get<unknown>('/pricebooks')
      .then((r) => setBooks(asList<PriceBook>(r, 'pricebooks', 'price_books')))
      .catch((e) => setError(errorText(e)))
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const c = await api.post<Customer>('/customers', {
        slug: slug.trim().toLowerCase(),
        name: name.trim(),
        admin_email: adminEmail.trim().toLowerCase(),
        price_book_id: priceBookId || null,
        billing_mode: billingMode,
        start_date: startDate || null,
      })
      nav(`/customers/${c.id}`)
    } catch (err) {
      setError(errorText(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ maxWidth: 560 }}>
      <h1>New customer</h1>
      <form className="card" onSubmit={submit}>
        <div className="grid2">
          <Field label="Slug">
            <input value={slug} onChange={(e) => setSlug(e.target.value)} pattern="[a-z0-9][a-z0-9-]*" required />
          </Field>
          <Field label="Name">
            <input value={name} onChange={(e) => setName(e.target.value)} required />
          </Field>
        </div>
        <Field label="Admin email">
          <input type="email" value={adminEmail} onChange={(e) => setAdminEmail(e.target.value)} required />
        </Field>
        <div className="grid2">
          <Field label="Price book">
            <select value={priceBookId} onChange={(e) => setPriceBookId(e.target.value)}>
              <option value="">— none —</option>
              {books.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name} ({b.currency})
                </option>
              ))}
            </select>
          </Field>
          <Field label="Billing mode">
            <select value={billingMode} onChange={(e) => setBillingMode(e.target.value)}>
              <option value="showback">showback</option>
              <option value="chargeback">chargeback</option>
              <option value="real">real</option>
            </select>
          </Field>
        </div>
        <Field label="Start date">
          <input type="date" value={startDate} onChange={(e) => setStartDate(e.target.value)} />
        </Field>
        {error ? <Notice kind="bad">{error}</Notice> : null}
        <button className="primary" disabled={busy}>
          Create
        </button>
      </form>
    </div>
  )
}
