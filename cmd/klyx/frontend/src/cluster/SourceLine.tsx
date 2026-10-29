// Settings S3b §3.4: where a view's metrics or log history come from and
// what makes them this cluster's - the scope sentence - and, under a
// scope, an empty answer said as such, never as a zero. Data kept on
// screen from an earlier read carries that read's sentence, qualified.
export function SourceLine({ subject, sentence, noData, stale, testid }: { subject?: string; sentence?: string; noData?: string; stale?: boolean; testid?: string }) {
  if (!sentence && !noData) return null;
  return (
    <div className="t-secondary" data-testid={testid ?? "source-line"} style={{ fontSize: 11 }}>
      {subject && `${subject}: `}
      {sentence && <span data-source-sentence>{sourceText(sentence, stale)}</span>}
      {sentence && noData ? " · " : ""}
      {noData && <span data-no-data>{noData}</span>}
    </div>
  );
}

// sourceText is a read's sentence as said: "Read from …", or, for data kept
// from an earlier read, "Earlier read from …, not current".
export function sourceText(sentence: string, stale?: boolean): string {
  return stale ? `Earlier read ${sentence}, not current` : `Read ${sentence}`;
}

// A range read as its charts carry it (SparklinesDTO's source fields).
type ChartRead = { available: boolean; sentence?: string; lineage?: string; noData?: string };

// chartSentence is what a range read's charts say about their own source
// (Settings S3b §3.2): nothing where the view's current readout already
// says it (the same lineage); kept as an earlier read where that readout
// now reads another source; their own sentence where there is no current
// readout.
export function chartSentence(spark: ChartRead, readout: { lineage?: string } | null): { sentence?: string; stale: boolean } {
  if (readout && readout.lineage === spark.lineage) return { stale: false };
  return { sentence: spark.sentence, stale: !!readout };
}

// ChartSource is a range read's own source and no-data caption where its
// charts are shown; readout is the view's current, available readout.
export function ChartSource({ spark, readout, testid }: { spark: ChartRead | null; readout: { lineage?: string } | null; testid?: string }) {
  if (!spark?.available) return null;
  const { sentence, stale } = chartSentence(spark, readout);
  return <SourceLine subject="30m charts" sentence={sentence} stale={stale} noData={spark.noData} testid={testid ?? "chart-source"} />;
}
