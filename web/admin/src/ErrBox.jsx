// Load-failure notice with retry — screens must never spin forever on a dead
// or erroring server.
export default function ErrBox({ what, err, onRetry }) {
  return (
    <div className="errbox">
      <div>{what} failed to load</div>
      <small>{err}</small>
      <button onClick={onRetry}>Retry</button>
    </div>
  )
}
