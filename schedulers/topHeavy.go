package schedulers

import (
	"bitbucket.org/sunybingcloud/electron/constants"
	"bitbucket.org/sunybingcloud/electron/def"
	"bitbucket.org/sunybingcloud/electron/utilities/mesosUtils"
	"bitbucket.org/sunybingcloud/electron/utilities/offerUtils"
	"fmt"
	"github.com/golang/protobuf/proto"
	mesos "github.com/mesos/mesos-go/mesosproto"
	"github.com/mesos/mesos-go/mesosutil"
	sched "github.com/mesos/mesos-go/scheduler"
	"log"
	"math"
	"os"
	"sort"
	"time"
)

/*
Tasks are categorized into small and large tasks based on the watts requirement.
All the large tasks are packed into offers from agents belonging to power class A and power class B, using BinPacking.
All the small tasks are spread among the offers from agents belonging to power class C, using FirstFit.

This was done to give a little more room for the large tasks (power intensive) for execution and reduce the possibility of
starvation of power intensive tasks.
*/

// electronScheduler implements the Scheduler interface
type TopHeavy struct {
	base                   // Type embedded to inherit common functions
	smallTasks, largeTasks []def.Task
}

// New electron scheduler
func NewTopHeavy(tasks []def.Task, wattsAsAResource bool, schedTracePrefix string, classMapWatts bool) *TopHeavy {
	sort.Sort(def.WattsSorter(tasks))

	logFile, err := os.Create("./" + schedTracePrefix + "_schedTrace.log")
	if err != nil {
		log.Fatal(err)
	}

	// Separating small tasks from large tasks.
	// Classification done based on MMPU watts requirements.
	mid := int(math.Floor((float64(len(tasks)) / 2.0) + 0.5))
	s := &TopHeavy{
		base: base{
			wattsAsAResource: wattsAsAResource,
			classMapWatts:    classMapWatts,
			Shutdown:         make(chan struct{}),
			Done:             make(chan struct{}),
			PCPLog:           make(chan struct{}),
			running:          make(map[string]map[string]bool),
			RecordPCP:        false,
			schedTrace:       log.New(logFile, "", log.LstdFlags),
		},
		smallTasks: tasks[:mid],
		largeTasks: tasks[mid+1:],
	}
	return s
}

func (s *TopHeavy) newTask(offer *mesos.Offer, task def.Task) *mesos.TaskInfo {
	taskName := fmt.Sprintf("%s-%d", task.Name, *task.Instances)
	s.tasksCreated++

	if !s.RecordPCP {
		// Turn on logging
		s.RecordPCP = true
		time.Sleep(1 * time.Second) // Make sure we're recording by the time the first task starts
	}

	// If this is our first time running into this Agent
	if _, ok := s.running[offer.GetSlaveId().GoString()]; !ok {
		s.running[offer.GetSlaveId().GoString()] = make(map[string]bool)
	}

	// Add task to list of tasks running on node
	s.running[offer.GetSlaveId().GoString()][taskName] = true

	resources := []*mesos.Resource{
		mesosutil.NewScalarResource("cpus", task.CPU),
		mesosutil.NewScalarResource("mem", task.RAM),
	}

	if s.wattsAsAResource {
		if wattsToConsider, err := def.WattsToConsider(task, s.classMapWatts, offer); err == nil {
			log.Printf("Watts considered for host[%s] and task[%s] = %f", *offer.Hostname, task.Name, wattsToConsider)
			resources = append(resources, mesosutil.NewScalarResource("watts", wattsToConsider))
		} else {
			// Error in determining wattsConsideration
			log.Fatal(err)
		}
	}

	return &mesos.TaskInfo{
		Name: proto.String(taskName),
		TaskId: &mesos.TaskID{
			Value: proto.String("electron-" + taskName),
		},
		SlaveId:   offer.SlaveId,
		Resources: resources,
		Command: &mesos.CommandInfo{
			Value: proto.String(task.CMD),
		},
		Container: &mesos.ContainerInfo{
			Type: mesos.ContainerInfo_DOCKER.Enum(),
			Docker: &mesos.ContainerInfo_DockerInfo{
				Image:   proto.String(task.Image),
				Network: mesos.ContainerInfo_DockerInfo_BRIDGE.Enum(), // Run everything isolated
			},
		},
	}
}

// Shut down scheduler if no more tasks to schedule
func (s *TopHeavy) shutDownIfNecessary() {
	if len(s.smallTasks) <= 0 && len(s.largeTasks) <= 0 {
		log.Println("Done scheduling all tasks")
		close(s.Shutdown)
	}
}

// create TaskInfo and log scheduling trace
func (s *TopHeavy) createTaskInfoAndLogSchedTrace(offer *mesos.Offer, task def.Task) *mesos.TaskInfo {
	log.Println("Co-Located with:")
	coLocated(s.running[offer.GetSlaveId().GoString()])
	taskToSchedule := s.newTask(offer, task)

	fmt.Println("Inst: ", *task.Instances)
	s.schedTrace.Print(offer.GetHostname() + ":" + taskToSchedule.GetTaskId().GetValue())
	*task.Instances--
	return taskToSchedule
}

// Using BinPacking to pack small tasks into this offer.
func (s *TopHeavy) pack(offers []*mesos.Offer, driver sched.SchedulerDriver) {
	for _, offer := range offers {
		select {
		case <-s.Shutdown:
			log.Println("Done scheduling tasks: declining offer on [", offer.GetHostname(), "]")
			driver.DeclineOffer(offer.Id, mesosUtils.LongFilter)

			log.Println("Number of tasks still running: ", s.tasksRunning)
			continue
		default:
		}

		tasks := []*mesos.TaskInfo{}
		offerCPU, offerRAM, offerWatts := offerUtils.OfferAgg(offer)
		totalWatts := 0.0
		totalCPU := 0.0
		totalRAM := 0.0
		taken := false
		for i := 0; i < len(s.smallTasks); i++ {
			task := s.smallTasks[i]
			wattsConsideration, err := def.WattsToConsider(task, s.classMapWatts, offer)
			if err != nil {
				// Error in determining wattsConsideration
				log.Fatal(err)
			}

			for *task.Instances > 0 {
				// Does the task fit
				// OR lazy evaluation. If ignore watts is set to true, second statement won't
				// be evaluated.
				if (!s.wattsAsAResource || (offerWatts >= (totalWatts + wattsConsideration))) &&
					(offerCPU >= (totalCPU + task.CPU)) &&
					(offerRAM >= (totalRAM + task.RAM)) {
					taken = true
					totalWatts += wattsConsideration
					totalCPU += task.CPU
					totalRAM += task.RAM
					tasks = append(tasks, s.createTaskInfoAndLogSchedTrace(offer, task))

					if *task.Instances <= 0 {
						// All instances of task have been scheduled, remove it
						s.smallTasks = append(s.smallTasks[:i], s.smallTasks[i+1:]...)
						s.shutDownIfNecessary()
					}
				} else {
					break // Continue on to next task
				}
			}
		}

		if taken {
			log.Printf("Starting on [%s]\n", offer.GetHostname())
			driver.LaunchTasks([]*mesos.OfferID{offer.Id}, tasks, mesosUtils.DefaultFilter)
		} else {
			// If there was no match for the task
			fmt.Println("There is not enough resources to launch a task:")
			cpus, mem, watts := offerUtils.OfferAgg(offer)

			log.Printf("<CPU: %f, RAM: %f, Watts: %f>\n", cpus, mem, watts)
			driver.DeclineOffer(offer.Id, mesosUtils.DefaultFilter)
		}
	}
}

// Using first fit to spread large tasks into these offers.
func (s *TopHeavy) spread(offers []*mesos.Offer, driver sched.SchedulerDriver) {
	for _, offer := range offers {
		select {
		case <-s.Shutdown:
			log.Println("Done scheduling tasks: declining offer on [", offer.GetHostname(), "]")
			driver.DeclineOffer(offer.Id, mesosUtils.LongFilter)

			log.Println("Number of tasks still running: ", s.tasksRunning)
			continue
		default:
		}

		tasks := []*mesos.TaskInfo{}
		offerCPU, offerRAM, offerWatts := offerUtils.OfferAgg(offer)
		offerTaken := false
		for i := 0; i < len(s.largeTasks); i++ {
			task := s.largeTasks[i]
			wattsConsideration, err := def.WattsToConsider(task, s.classMapWatts, offer)
			if err != nil {
				// Error in determining wattsConsideration
				log.Fatal(err)
			}

			// Decision to take the offer or not
			if (!s.wattsAsAResource || (offerWatts >= wattsConsideration)) &&
				(offerCPU >= task.CPU) && (offerRAM >= task.RAM) {
				offerTaken = true
				tasks = append(tasks, s.createTaskInfoAndLogSchedTrace(offer, task))
				log.Printf("Starting %s on [%s]\n", task.Name, offer.GetHostname())
				driver.LaunchTasks([]*mesos.OfferID{offer.Id}, tasks, mesosUtils.DefaultFilter)

				if *task.Instances <= 0 {
					// All instances of task have been scheduled, remove it
					s.largeTasks = append(s.largeTasks[:i], s.largeTasks[i+1:]...)
					s.shutDownIfNecessary()
				}
				break // Offer taken, move on
			}
		}

		if !offerTaken {
			// If there was no match for the task
			fmt.Println("There is not enough resources to launch a task:")
			cpus, mem, watts := offerUtils.OfferAgg(offer)

			log.Printf("<CPU: %f, RAM: %f, Watts: %f>\n", cpus, mem, watts)
			driver.DeclineOffer(offer.Id, mesosUtils.DefaultFilter)
		}
	}
}

func (s *TopHeavy) ResourceOffers(driver sched.SchedulerDriver, offers []*mesos.Offer) {
	log.Printf("Received %d resource offers", len(offers))

	// We need to separate the offers into
	// offers from ClassA and ClassB and offers from ClassC.
	// Offers from ClassA and ClassB would execute the large tasks.
	// Offers from ClassC would execute the small tasks.
	offersClassAB := []*mesos.Offer{}
	offersClassC := []*mesos.Offer{}

	for _, offer := range offers {
		select {
		case <-s.Shutdown:
			log.Println("Done scheduling tasks: declining offer on [", offer.GetHostname(), "]")
			driver.DeclineOffer(offer.Id, mesosUtils.LongFilter)

			log.Println("Number of tasks still running: ", s.tasksRunning)
			continue
		default:
		}

		if constants.PowerClasses["A"][*offer.Hostname] ||
			constants.PowerClasses["B"][*offer.Hostname] {
			offersClassAB = append(offersClassAB, offer)
		} else if constants.PowerClasses["C"][*offer.Hostname] {
			offersClassC = append(offersClassC, offer)
		}
	}

	log.Println("ClassAB Offers:")
	for _, o := range offersClassAB {
		log.Println(*o.Hostname)
	}
	log.Println("ClassC Offers:")
	for _, o := range offersClassC {
		log.Println(*o.Hostname)
	}

	// Packing tasks into offersClassC
	s.pack(offersClassC, driver)
	// Spreading tasks among offersClassAB
	s.spread(offersClassAB, driver)
}

func (s *TopHeavy) StatusUpdate(driver sched.SchedulerDriver, status *mesos.TaskStatus) {
	log.Printf("Received task status [%s] for task [%s]", NameFor(status.State), *status.TaskId.Value)

	if *status.State == mesos.TaskState_TASK_RUNNING {
		s.tasksRunning++
	} else if IsTerminal(status.State) {
		delete(s.running[status.GetSlaveId().GoString()], *status.TaskId.Value)
		s.tasksRunning--
		if s.tasksRunning == 0 {
			select {
			case <-s.Shutdown:
				close(s.Done)
			default:
			}
		}
	}
	log.Printf("DONE: Task status [%s] for task [%s]", NameFor(status.State), *status.TaskId.Value)
}
