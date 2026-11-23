import { Route } from "react-router-dom";
import MigrationPage from "./Definitions";
import History from "./History";
import { Queue, Review } from "../approvals/Review";
import JobPage from "../jobs/Job";
import "./workflows.css";

export const migrationRoutes = (
  <>
    <Route path="/databases/:db/migrations" element={<MigrationPage />} />
    <Route path="/databases/:db/history" element={<History />} />
    <Route path="/requests" element={<Queue />} />
    <Route path="/requests/:id" element={<Review />} />
    <Route path="/jobs/:id" element={<JobPage />} />
  </>
);
