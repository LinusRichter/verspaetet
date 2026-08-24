export interface Station { eva: string; slug: string; name: string; category: number | null; stop_events: number; }
export interface Line { line_label: string; line_category: string; stop_events: number; avg_delay_s: number; }
export interface StopEvent {
  id: number; direction: string;
  line_label: string; line_category: string;
  direction_name: string | null; direction_slug: string | null; via_slugs: string[];
  trip_uuid: string; trip_id: string; trip_date: string;
  station_name: string | null;
  planned_time: string; actual_time: string | null;
  delay_s: number; platform: string | null; planned_platform: string | null;
  notes: string | null; scraped_at: string;
}
export interface TripStop {
  station_eva: string; station_slug: string; station_name: string;
  direction: string; line_label: string; line_category: string;
  planned_time: string; actual_time: string | null;
  delay_s: number; platform: string | null; planned_platform: string | null;
  scraped_at: string;
}
export interface Route {
  line_label: string; line_category: string;
  direction_name: string | null; direction_slug: string | null;
  via_slugs: string[]; stop_events: number; avg_delay_s: number;
}
export interface TopDelay { station_name: string; line_label: string; direction_name: string | null; planned_time: string; actual_time: string | null; delay_s: number; scraped_at: string; }
export interface Stats { stations: number; stop_events: number; delayed: number; avg_delay_s: number; max_delay_s: number; }
export interface Health { recent_runs: number; recent_events: number; last_scrape_ago_s: number; expected_runs: number; fetch_rate: number; }