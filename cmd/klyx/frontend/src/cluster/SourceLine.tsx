// Settings S3b §3.4: where a view's metrics or log history come from and
// what makes them this cluster's - the scope sentence - and, under a
// scope, an empty answer said as such, never as a zero. Data kept on
// screen from an earlier read carries that read's sentence, qualified.
export function SourceLine({ sentence, noData, stale, testid }: { sentence?: string; noData?: string; stale?: boolean; testid?: string }) {
  if (!sentence && !noData) return null;
  return (
    <div className="t-secondary" data-testid={testid ?? "source-line"} style={{ fontSize: 11 }}>
      {sentence && <span data-source-sentence>{stale ? `Earlier read ${sentence}, not current` : `Read ${sentence}`}</span>}
      {sentence && noData ? " · " : ""}
      {noData && <span data-no-data>{noData}</span>}
    </div>
  );
}
