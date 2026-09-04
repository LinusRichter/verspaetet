export interface Station { eva: string; slug: string; name: string; category: number | null; lat: number | null; lon: number | null; federal_state: string | null; stop_events: number; }
export interface Line { line_category: string; train_number: string; stop_events: number; avg_delay_s: number; }
export interface StopEvent {
  id: number; direction: string;
  line_category: string; train_number: string | null; owner: string | null;
  stop_id: string;
  direction_name: string | null; via_path: string[];
  station_name: string | null;
  planned_time: string; actual_time: string | null;
  delay_s: number | null; platform: string | null; planned_platform: string | null;
  cancelled: boolean; scraped_at: string;
}
export interface TripStop {
  station_eva: string; station_name: string;
  line_category: string; train_number: string | null;
  planned_time: string; actual_time: string | null;
  delay_s: number | null; platform: string | null; planned_platform: string | null;
  scraped_at: string;
}
export interface Route {
  line_category: string; train_number: string;
  direction_name: string | null;
  via_path: string[]; stop_events: number; avg_delay_s: number;
}
export interface TopDelay { station_name: string; line_category: string; train_number: string | null; direction_name: string | null; planned_time: string; actual_time: string | null; delay_s: number; scraped_at: string; }
export interface Stats { stations: number; stop_events: number; delayed: number; avg_delay_s: number; max_delay_s: number; }
export interface Health { recent_runs: number; recent_events: number; last_scrape_ago_s: number; expected_runs: number; fetch_rate: number; }