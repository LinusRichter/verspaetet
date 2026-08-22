interface Props {
  stationName: string | null
  lineLabel: string | null
  onReset: () => void
  onResetLine: () => void
}

export function Breadcrumb({ stationName, lineLabel, onReset, onResetLine }: Props) {
  return (
    <div className="breadcrumb">
      <span className="crumb clickable" onClick={onReset}>Stations</span>
      {stationName && (
        <>
          <span className="crumb-sep">›</span>
          <span className="crumb clickable" onClick={onResetLine}>{stationName}</span>
        </>
      )}
      {lineLabel && (
        <>
          <span className="crumb-sep">›</span>
          <span className="crumb">{lineLabel}</span>
        </>
      )}
    </div>
  )
}