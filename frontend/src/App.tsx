import { useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import './App.css'

type Overview = { partners: number; parties: number; purchases: number; products: number }
type Partner = { id: string; name: string; partner_id: string; api_key: string; created_at: string }
type PartyProfile = { id: string; name: string; party_type: string; display_id: string; aliases: string[] | null; created_at: string }
type Identifier = { scheme: string; value: string }
type Product = { id: string; manufacturer: string; model: string; identifiers: Identifier[]; resources: unknown[]; total_amount: string | null; vat_amount: string | null }
type Purchase = { id: string; order_ref: string; occurred_at: string; products: Product[]; currency: string | null; total_amount: string | null; vat_amount: string | null }
type ApiField = { name: string; location: string; required: boolean; type: string; note?: string }
type ApiEndpoint = { method: string; path: string; summary: string; auth: string; request_body?: string; fields: ApiField[]; template?: unknown; product_template?: unknown }

async function loggedFetch(path: string, options?: RequestInit, route = path): Promise<Response> {
  const method = options?.method ?? 'GET'
  const started = performance.now()
  console.info('[API] request', { method, route })
  try {
    const response = await fetch(path, options)
    const details = {
      method, route, status: response.status,
      durationMs: Math.round(performance.now() - started),
      correlationId: response.headers.get('X-Correlation-ID'),
    }
    if (response.ok) console.info('[API] response', details)
    else console.warn('[API] response', details)
    return response
  } catch (error) {
    console.error('[API] network failure', { method, route, durationMs: Math.round(performance.now() - started) })
    throw error
  }
}

async function api<T>(path: string, options?: RequestInit, route?: string): Promise<T> {
  const response = await loggedFetch(path, { ...options, headers: { 'Content-Type': 'application/json', ...options?.headers } }, route)
  const body = await response.json()
  if (!response.ok) throw new Error(body.detail || body.title || 'Request failed')
  return body as T
}

function App() {
  const [view, setView] = useState<'admin' | 'party' | 'api'>('admin')
  return <div className="shell">
    <header className="topbar">
      <div className="brand"><span className="brand-mark">P</span><span>Product & Ownership Register</span></div>
      <nav>
        <button className={view === 'admin' ? 'active' : ''} onClick={() => setView('admin')}>Admin</button>
        <button className={view === 'party' ? 'active' : ''} onClick={() => setView('party')}>My account</button>
        <button className={view === 'api' ? 'active' : ''} onClick={() => setView('api')}>API console</button>
      </nav>
    </header>
    {view === 'admin' ? <Admin /> : view === 'party' ? <PartyAccount /> : <ApiConsole />}
  </div>
}

function Admin() {
  const [overview, setOverview] = useState<Overview>()
  const [partners, setPartners] = useState<Partner[]>([])
  const [profiles, setProfiles] = useState<PartyProfile[]>([])
  const [message, setMessage] = useState('')
  const [partnerName, setPartnerName] = useState('')
  const [partnerRef, setPartnerRef] = useState('')
  const [partyName, setPartyName] = useState('')
  const [partyId, setPartyId] = useState('')
  const [partyType, setPartyType] = useState('PERSON')
  const [aliases, setAliases] = useState('')
  const [editingId, setEditingId] = useState<string>()
  const [savingParty, setSavingParty] = useState(false)

  const refresh = () => {
    Promise.all([api<Overview>('/api/v1/overview'), api<Partner[]>('/api/v1/partners'), api<PartyProfile[]>('/api/v1/party-profiles')])
      .then(([counts, partnerRows, partyRows]) => { setOverview(counts); setPartners(partnerRows); setProfiles(partyRows) })
      .catch(error => setMessage((error as Error).message))
  }
  useEffect(refresh, [])

  const createPartner = async (event: FormEvent) => {
    event.preventDefault()
    try {
      const created = await api<Partner & { api_key: string }>('/api/v1/partners', { method: 'POST', body: JSON.stringify({ name: partnerName, partner_id: partnerRef }) })
      setMessage(`Partner created. API key: ${created.api_key}`)
      setPartnerName(''); setPartnerRef(''); refresh()
    } catch (error) { setMessage((error as Error).message) }
  }

  const resetPartyForm = () => {
    setEditingId(undefined); setPartyName(''); setPartyId(''); setPartyType('PERSON'); setAliases('')
  }

  const editParty = (party: PartyProfile) => {
    setEditingId(party.id); setPartyName(party.name); setPartyId(party.display_id)
    setPartyType(party.party_type); setAliases((party.aliases ?? []).join(', ')); setMessage('')
  }

  const savePartyProfile = async (event: FormEvent) => {
    event.preventDefault()
    setSavingParty(true)
    try {
      const parsedAliases = aliases.split(',').map(item => item.trim()).filter(Boolean)
      const path = editingId ? `/api/v1/party-profiles/${editingId}` : '/api/v1/party-profiles'
      await api(path, { method: editingId ? 'PUT' : 'POST', body: JSON.stringify({ name: partyName, party_type: partyType, display_id: partyId, aliases: parsedAliases }) }, editingId ? '/api/v1/party-profiles/{partyID}' : path)
      setMessage(editingId ? 'Party profile updated' : 'Party profile created')
      resetPartyForm(); refresh()
    } catch (error) { setMessage((error as Error).message) }
    finally { setSavingParty(false) }
  }

  return <main>
    <div className="page-heading"><div><p className="eyebrow">Administration</p><h1>Register overview</h1></div></div>
    {message && <div className="notice">{message}</div>}
    <section className="metrics">
      <Metric label="Partners" value={overview?.partners ?? 0} />
      <Metric label="Party profiles" value={overview?.parties ?? 0} />
      <Metric label="Purchases" value={overview?.purchases ?? 0} />
      <Metric label="Products" value={overview?.products ?? 0} />
    </section>
    <div className="grid">
      <section className="card"><h2>Create partner</h2><form onSubmit={createPartner}>
        <label>Name<input value={partnerName} onChange={e => setPartnerName(e.target.value)} placeholder="Partner name" required /></label>
        <label>Partner ID<input value={partnerRef} onChange={e => setPartnerRef(e.target.value)} placeholder="partner-1" required /></label>
        <button type="submit">Generate partner & ID key</button>
      </form></section>
      <section className="card"><h2>{editingId ? 'Update party profile' : 'Create party profile'}</h2><form onSubmit={savePartyProfile}>
        <label>Name<input value={partyName} onChange={e => setPartyName(e.target.value)} placeholder="Anna Andersson" required /></label>
        <label>Party ID<input value={partyId} onChange={e => setPartyId(e.target.value)} placeholder="party-1001" required /></label>
        <label>Type<select value={partyType} onChange={e => setPartyType(e.target.value)}><option>PERSON</option><option>ORGANIZATION</option><option>UNKNOWN</option></select></label>
        <label>Aliases <span className="optional">comma-separated party aliases</span><input value={aliases} onChange={e => setAliases(e.target.value)} placeholder="nickname, customer-42" /></label>
        <button type="submit" disabled={savingParty}>{savingParty ? 'Saving…' : editingId ? 'Save changes' : 'Create independent party'}</button>
        {editingId && <button type="button" className="secondary" disabled={savingParty} onClick={resetPartyForm}>Cancel editing</button>}
      </form></section>
    </div>
    <div className="grid">
      <TableCard title="Partners" columns={['Name', 'Partner ID', 'API key']} rows={partners.map(item => [item.name, item.partner_id, item.api_key])} />
      <section className="card"><h2>Party profiles</h2><div className="table-wrap"><table>
        <thead><tr><th>Name</th><th>Party ID</th><th>Type</th><th>Aliases</th></tr></thead>
        <tbody>{profiles.map(item => <tr key={item.id} className="selectable-row" onClick={() => { if (!savingParty) editParty(item) }}>
          <td><button type="button" className="row-button" disabled={savingParty} aria-pressed={editingId === item.id}>{item.name}</button></td>
          <td>{item.display_id}</td><td>{item.party_type}</td><td>{item.aliases?.join(', ') || 'None'}</td>
        </tr>)}</tbody>
      </table>{profiles.length === 0 && <p className="empty">No records yet</p>}</div></section>
    </div>
  </main>
}

function Metric({ label, value }: { label: string; value: number }) {
  return <div className="metric"><span>{label}</span><strong>{value}</strong></div>
}

function TableCard({ title, columns, rows }: { title: string; columns: string[]; rows: string[][] }) {
  return <section className="card"><h2>{title}</h2><div className="table-wrap"><table><thead><tr>{columns.map(column => <th key={column}>{column}</th>)}</tr></thead><tbody>{rows.map((row, index) => <tr key={index}>{row.map((cell, cellIndex) => <td key={cellIndex}>{cell}</td>)}</tr>)}</tbody></table>{rows.length === 0 && <p className="empty">No records yet</p>}</div></section>
}

function PartyAccount() {
  const [profiles, setProfiles] = useState<PartyProfile[]>([])
  const [selectedId, setSelectedId] = useState('')
  const [purchases, setPurchases] = useState<Purchase[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    api<PartyProfile[]>('/api/v1/party-profiles').then(rows => setProfiles(rows)).catch(error => setError((error as Error).message))
  }, [])

  const loadPurchases = async (profileId: string) => {
    setSelectedId(profileId)
    try {
      setPurchases(await api<Purchase[]>(`/api/v1/party/${profileId}/purchases`, undefined, '/api/v1/party/{partyID}/purchases'))
      setError('')
    } catch (loadError) { setError((loadError as Error).message) }
  }

  const selected = profiles.find(profile => profile.id === selectedId)
  return <main>
    <div className="page-heading"><div><p className="eyebrow">Party login</p><h1>My purchases & products</h1></div></div>
    <section className="card account-picker">
      <label>Select party profile<select value={selectedId} onChange={e => loadPurchases(e.target.value)}>
        <option value="">Choose who should be shown</option>
        {profiles.map(profile => <option key={profile.id} value={profile.id}>{profile.name} — {profile.display_id}</option>)}
      </select></label>
      {error && <p className="error">{error}</p>}
    </section>
    {selected && purchases.map(purchase => <section className="card purchase" key={purchase.id}>
      <div className="purchase-heading"><div><p className="eyebrow">Purchase</p><h2>{purchase.order_ref}</h2></div><span>{new Date(purchase.occurred_at).toLocaleDateString()}</span></div>
      {purchase.total_amount !== null && <p>Total: {purchase.total_amount} {purchase.currency} · VAT: {purchase.vat_amount} {purchase.currency}</p>}
      {purchase.products.length === 0 && <p className="muted">No products attached yet.</p>}
      {purchase.products.map(product => <div className="product" key={product.id}><div className="product-icon">P</div><div><h3>{product.manufacturer || 'Product'} {product.model}</h3>{product.total_amount !== null && <p>Total: {product.total_amount} {purchase.currency} · VAT: {product.vat_amount} {purchase.currency}</p>}{product.identifiers.map((identifier, index) => <code key={index}>{identifier.scheme}: {identifier.value}</code>)}</div></div>)}
    </section>)}
    {selected && purchases.length === 0 && <section className="card"><p className="empty">No purchases are linked to this party.</p></section>}
  </main>
}

function ApiConsole() {
  const [partners, setPartners] = useState<Partner[]>([])
  const [endpoints, setEndpoints] = useState<ApiEndpoint[]>([])
  const [selectedPath, setSelectedPath] = useState('')
  const [partnerId, setPartnerId] = useState('')
  const [bodies, setBodies] = useState<Record<string, string>>({})
  const [orderRef, setOrderRef] = useState('')
  const [result, setResult] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    Promise.all([api<Partner[]>('/api/v1/partners'), api<ApiEndpoint[]>('/api/v1/swagger/endpoints')])
      .then(([partnerRows, endpointRows]) => {
        setPartners(partnerRows)
        setEndpoints(endpointRows)
        setBodies(Object.fromEntries(endpointRows.filter(endpoint => endpoint.template !== undefined).map(endpoint => [endpoint.path, JSON.stringify(endpoint.template, null, 2)])))
        setSelectedPath(endpointRows[0]?.path || '')
      })
      .catch(loadError => setError((loadError as Error).message))
  }, [])

  const selected = endpoints.find(endpoint => endpoint.path === selectedPath)
  const partner = partners.find(item => item.partner_id === partnerId)
  const send = async () => {
    if (!selected) return
    setError(''); setResult('')
    try {
      if (selected.path.includes('{order_ref}') && !orderRef.trim()) {
        throw new Error('Order reference is required.')
      }
      const path = selected.path.replace('{order_ref}', encodeURIComponent(orderRef.trim()))
      const response = await loggedFetch(path, {
        method: selected.method,
        headers: {
          'Content-Type': 'application/json',
          ...(partner?.api_key ? { Authorization: `Bearer ${partner.api_key}` } : {}),
        },
        ...(selected.request_body ? { body: bodies[selected.path] } : {}),
      }, selected.path)
      const text = await response.text()
      setResult(`${response.status} ${response.statusText}\n${text}`)
    } catch (sendError) { setError((sendError as Error).message) }
  }

  return <main>
    <div className="page-heading"><div><p className="eyebrow">API console</p><h1>Partner endpoints</h1></div></div>
    <div className="grid api-grid">
      <section className="card">
        <label>Endpoint<select value={selectedPath} onChange={e => setSelectedPath(e.target.value)}>{endpoints.map(endpoint => <option key={endpoint.path} value={endpoint.path}>{endpoint.method} {endpoint.path}</option>)}</select></label>
        {selected && <><div className="endpoint-summary"><span className={`method ${selected.method.toLowerCase()}`}>{selected.method}</span><code>{selected.path}</code><p>{selected.summary}</p></div>
        {selected.auth !== 'None' && <><label>Partner <span className="field-required">Required · authorization</span><select value={partnerId} onChange={e => setPartnerId(e.target.value)}><option value="">Choose a partner</option>{partners.map(item => <option key={item.id} value={item.partner_id}>{item.name} — {item.partner_id}</option>)}</select></label>
        {partner && <label>API key (autofilled)<input value={partner.api_key} readOnly /></label>}</>}
        {selected.path.includes('{order_ref}') && <label>Order reference <span className="field-required">Required · path</span><input value={orderRef} onChange={e => setOrderRef(e.target.value)} /></label>}
        {selected.request_body && <>
          <label>Request JSON<textarea value={bodies[selected.path] ?? ''} onChange={e => setBodies(current => ({ ...current, [selected.path]: e.target.value }))} rows={18} /></label>
          <p className="muted">Fill in the empty values. {selected.path === '/partner/v1/orders' ? 'Products may be omitted or empty.' : 'At least one product is required.'} Resources may be omitted or replaced with <code>[]</code>. Required nested fields apply when their containing object is included.</p>
          {selected.product_template !== undefined && <details><summary>Product example (optional)</summary><pre>{JSON.stringify(selected.product_template, null, 2)}</pre></details>}
        </>}
        {selected.fields.length > 0 && <div className="fields"><h3>Request fields</h3>{selected.fields.map(field => <p key={`${field.location}:${field.name}`}><code>{field.name}</code> <span>{field.location}</span> · {field.type} · <strong className={field.required ? 'field-required' : 'field-optional'}>{field.required ? 'Required' : 'Optional'}</strong>{field.note && <> — {field.note}</>}</p>)}</div>}
        <button onClick={send}>Send request</button></>}
      </section>
      <section className="card response"><h2>Response</h2>{error && <p className="error">{error}</p>}<pre>{result || 'Response output will appear here.'}</pre></section>
    </div>
  </main>
}

export default App
