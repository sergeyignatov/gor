package main

import (
	"fmt"
	"log"
	"strconv"
	"time"
)

const (
	alrate = 1
)

type ALStat struct {
	latest int
	mean   int
	max    int
	min    int
	count  int
}

func NewALStat() (s *ALStat) {
	s = new(ALStat)
	s.latest = 0
	s.mean = 0
	s.max = 0
	s.min = 100000
	s.count = 0

	if Settings.stats {
		if !Settings.rawstat {
			log.Println("latest mean min max count/second")
		}
		go s.reportStats()
	}
	return
}

func (s *ALStat) Write(start int32, latest int) {
	if Settings.stats && !Settings.rawstat {
		if latest > s.max {
			s.max = latest
		}
		if latest < s.min {
			s.min = latest
		}
		if latest != 0 {
			s.mean = (s.mean + latest) / 2
		}
		s.latest = latest
		s.count = s.count + 1
	}
	if Settings.rawstat {
		fmt.Println(latest)
	} else if Settings.jtl {
		//timeStamp,elapsed,label,responseCode,responseMessage,threadName,dataType,success,bytes,URL,Latency
		fmt.Printf("%d,%d,test,200,OK,test,true,100,/,%d\n", start, latest, latest)
	}

}

func (s *ALStat) Reset() {
	s.latest = 0
	s.max = 0
	s.mean = 0
	s.count = 0
	s.min = 1000000
}

func (s *ALStat) String() string {
	return strconv.Itoa(s.latest) + " " + strconv.Itoa(s.mean) + " " + strconv.Itoa(s.min) + " " + strconv.Itoa(s.max) + " " + strconv.Itoa(s.count/alrate)
}

func (s *ALStat) reportStats() {
	for {
		if s.mean != 0 {
			fmt.Println(s)
		}
		s.Reset()
		time.Sleep(alrate * time.Second)
	}
}
